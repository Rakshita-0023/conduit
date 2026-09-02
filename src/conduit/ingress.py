"""Starlette handlers for Conduit's public local HTTP boundary."""

from __future__ import annotations

import json
from collections.abc import Mapping
from typing import TYPE_CHECKING, Awaitable, Callable

from starlette.requests import Request
from starlette.responses import PlainTextResponse, Response

from .config import Config
from .health import HealthState
from .protocol import (
    COMPAT_PROTOCOL_VERSIONS,
    MAX_INGRESS_BODY_BYTES,
    MCP_PROTOCOL_VERSION,
    CompatibilityRequest,
    ProtocolError,
    compatibility_initialize_result,
    discovery_result,
    json_rpc_error,
    json_rpc_result,
    validate_compatibility_request,
    validate_mcp_request,
)

if TYPE_CHECKING:
    from .app import Runtime


def healthz(state: HealthState) -> Callable[[Request], Awaitable[Response]]:
    async def handler(_: Request) -> Response:
        return _go_json(state.snapshot())

    return handler


def status(state: HealthState) -> Callable[[Request], Awaitable[Response]]:
    async def handler(_: Request) -> Response:
        return _go_json(state.snapshot())

    return handler


def mcp(runtime: "Runtime", build_version: str) -> Callable[[Request], Awaitable[Response]]:
    async def handler(request: Request) -> Response:
        if not _origin_allowed(request.headers.get("origin"), runtime.config):
            return PlainTextResponse("forbidden origin\n", status_code=403)
        if request.method == "GET":
            return _compatibility_get(runtime, request)
        if request.method == "DELETE":
            return _compatibility_delete(runtime, request)
        body = await _bounded_body(request)
        if body is None:
            return PlainTextResponse("invalid MCP request\n", status_code=400)
        try:
            message = validate_mcp_request(body, request.headers)
        except ProtocolError:
            return await _compatibility_post(runtime, build_version, request, body)

        if message.method not in {"server/discover", "tools/list", "tools/call"}:
            if (failure := _transport_failure(request)) is not None:
                return failure
            return Response(
                json_rpc_error(message.raw_id, -32601, "method not found"),
                status_code=404,
                media_type="application/json",
            )
        if message.method == "server/discover":
            if not runtime.health.snapshot()["ready"]:
                return PlainTextResponse("conduit not ready\n", status_code=503)
            if (failure := _transport_failure(request)) is not None:
                return failure
            return Response(
                json_rpc_result(message.raw_id, discovery_result(build_version)),
                media_type="application/json",
            )
        if (failure := _transport_failure(request)) is not None:
            return failure
        if message.method == "tools/list":
            return _tools_list(runtime, message.raw_id, message.params)
        return await _tools_call(runtime, request.headers, message.raw_id, message.params)

    return handler


def _tools_list(runtime: "Runtime", raw_id: str, params: Mapping[str, object]) -> Response:
    if params.get("cursor", ""):
        return Response(json_rpc_error(raw_id, -32602, "conduit does not support tools/list pagination"), media_type="application/json")
    snapshot = runtime.registry.snapshot
    if snapshot.state == "aggregate_over_limit":
        return Response(json_rpc_error(raw_id, -32603, "conduit catalog limit exceeded"), media_type="application/json")
    if snapshot.state == "aggregate_collision":
        return Response(json_rpc_error(raw_id, -32603, "conduit catalog unavailable"), media_type="application/json")
    if snapshot.state != "ready" or not runtime.health.snapshot()["ready"] or snapshot.result is None:
        return PlainTextResponse("conduit not ready\n", status_code=503)
    return Response(json_rpc_result(raw_id, snapshot.result), media_type="application/json")


async def _tools_call(
    runtime: "Runtime",
    headers: Mapping[str, str],
    raw_id: str,
    params: Mapping[str, object],
    *,
    compatibility: bool = False,
) -> Response:
    name = params.get("name")
    arguments = params.get("arguments", {})
    if not isinstance(name, str) or not name or not isinstance(arguments, dict):
        return PlainTextResponse("invalid MCP request\n", status_code=400)
    request_headers = dict(headers)
    supplied_name = _header_value(request_headers, "mcp-name")
    if compatibility:
        if supplied_name is not None and supplied_name != name:
            return PlainTextResponse("invalid MCP request\n", status_code=400)
        request_headers["mcp-name"] = name
    elif supplied_name != name:
        return PlainTextResponse("invalid MCP request\n", status_code=400)
    if not runtime.health.snapshot()["ready"]:
        return PlainTextResponse("conduit not ready\n", status_code=503)
    try:
        reply = await runtime.dispatcher.execute(name, arguments, params.get("inputResponses"), params.get("requestState"), request_headers)
    except Exception as exc:
        from .errors import GatewayError

        if isinstance(exc, GatewayError):
            return Response(json_rpc_error(raw_id, exc.code, exc.message), media_type="application/json")
        return Response(json_rpc_error(raw_id, -32011, "tool dispatch failed"), media_type="application/json")
    if "error" in reply:
        return Response(json_rpc_error(raw_id, reply["error"]["code"], reply["error"]["message"], reply["error"].get("data")), media_type="application/json")
    return Response(json_rpc_result(raw_id, reply["result"]), media_type="application/json")


async def _compatibility_post(runtime: "Runtime", build_version: str, request: Request, body: bytes) -> Response:
    """Adapt standard session-based Streamable HTTP requests at the edge.

    Modern requests are still validated above.  A request explicitly carrying a
    modern protocol header can never fall through to this adapter.
    """

    if request.headers.get("mcp-protocol-version") == MCP_PROTOCOL_VERSION or request.headers.get("mcp-method") is not None:
        return PlainTextResponse("invalid MCP request\n", status_code=400)
    if (failure := _transport_failure(request)) is not None:
        return failure
    try:
        message = validate_compatibility_request(body)
    except ProtocolError:
        return PlainTextResponse("invalid MCP request\n", status_code=400)

    if message.method == "initialize":
        return _compatibility_initialize(runtime, build_version, request, message)
    if message.method == "notifications/initialized":
        if message.raw_id is not None or not _valid_compatibility_session(runtime, request):
            return PlainTextResponse("invalid MCP request\n", status_code=400)
        return Response(status_code=202)
    if message.raw_id is None:
        return PlainTextResponse("invalid MCP request\n", status_code=400)
    if not _valid_compatibility_session(runtime, request):
        return PlainTextResponse("invalid MCP session\n", status_code=400)
    session_id = _session_id(request)
    assert session_id is not None
    if message.method == "tools/list":
        response = _compatibility_tools_list(runtime, message.raw_id, message.params)
    elif message.method == "tools/call":
        response = await _tools_call(runtime, request.headers, message.raw_id, message.params, compatibility=True)
    else:
        response = Response(json_rpc_error(message.raw_id, -32601, "method not found"), media_type="application/json")
    response.headers["Mcp-Session-Id"] = session_id
    return response


def _compatibility_initialize(
    runtime: "Runtime", build_version: str, request: Request, message: CompatibilityRequest
) -> Response:
    if message.raw_id is None or _session_id(request) is not None:
        return PlainTextResponse("invalid MCP request\n", status_code=400)
    protocol_version = message.params.get("protocolVersion")
    if not isinstance(protocol_version, str) or protocol_version not in COMPAT_PROTOCOL_VERSIONS:
        return Response(json_rpc_error(message.raw_id, -32602, "unsupported MCP protocol version"), media_type="application/json")
    session_id = runtime.compatibility_sessions.create(protocol_version)
    return Response(
        json_rpc_result(message.raw_id, compatibility_initialize_result(build_version, protocol_version)),
        media_type="application/json",
        headers={"Mcp-Session-Id": session_id},
    )


def _compatibility_tools_list(runtime: "Runtime", raw_id: str, params: Mapping[str, object]) -> Response:
    if params.get("cursor", ""):
        return Response(json_rpc_error(raw_id, -32602, "conduit does not support tools/list pagination"), media_type="application/json")
    snapshot = runtime.registry.snapshot
    if snapshot.state == "aggregate_over_limit":
        return Response(json_rpc_error(raw_id, -32603, "conduit catalog limit exceeded"), media_type="application/json")
    if snapshot.state == "aggregate_collision":
        return Response(json_rpc_error(raw_id, -32603, "conduit catalog unavailable"), media_type="application/json")
    if snapshot.state != "ready" or not runtime.health.snapshot()["ready"] or snapshot.result is None:
        return PlainTextResponse("conduit not ready\n", status_code=503)
    return Response(json_rpc_result(raw_id, {"tools": snapshot.tools}), media_type="application/json")


def _compatibility_get(runtime: "Runtime", request: Request) -> Response:
    """Provide a terminal, notification-free SSE channel for standard clients.

    Streamable HTTP clients use GET to establish their server-to-client event
    channel.  Conduit has no server notifications and intentionally does not
    bridge downstream progress, so the channel contains only a comment and is
    closed immediately.  Tool results always remain terminal JSON responses.
    """

    if not _valid_compatibility_session(runtime, request):
        return PlainTextResponse("invalid MCP session\n", status_code=400)
    return Response(
        b": conduit does not emit server events\n\n",
        media_type="text/event-stream",
        headers={"Mcp-Session-Id": _session_id(request) or "", "Cache-Control": "no-cache"},
    )


def _compatibility_delete(runtime: "Runtime", request: Request) -> Response:
    if not _valid_compatibility_session(runtime, request):
        return PlainTextResponse("invalid MCP session\n", status_code=400)
    session_id = _session_id(request)
    assert session_id is not None
    runtime.compatibility_sessions.remove(session_id)
    return Response(status_code=204)


def _valid_compatibility_session(runtime: "Runtime", request: Request) -> bool:
    return runtime.compatibility_sessions.valid(_session_id(request), request.headers.get("mcp-protocol-version"))


def _session_id(request: Request) -> str | None:
    return request.headers.get("mcp-session-id")


def _header_value(headers: Mapping[str, str], name: str) -> str | None:
    lowered_name = name.lower()
    for key, value in headers.items():
        if key.lower() == lowered_name:
            return value
    return None


async def _bounded_body(request: Request) -> bytes | None:
    """Read at most the same 1 MiB public ingress bound as the Go handler."""

    body = bytearray()
    async for chunk in request.stream():
        if len(chunk) > MAX_INGRESS_BODY_BYTES - len(body):
            return None
        body.extend(chunk)
    return bytes(body)


def _transport_failure(request: Request) -> Response | None:
    if not _is_json_content_type(request.headers.get("content-type")):
        return PlainTextResponse("unsupported media type\n", status_code=415)
    if not _accepts_terminal_json(request.headers.get("accept")):
        return PlainTextResponse("invalid Accept header\n", status_code=400)
    return None


def _go_json(value: object) -> Response:
    """Mirror the compact newline-terminated JSON emitted by Go's encoder."""

    return Response(json.dumps(value, separators=(",", ":"), ensure_ascii=False) + "\n", media_type="application/json")


def _origin_allowed(origin: str | None, config: Config) -> bool:
    if origin is None:
        return True
    return origin in config.listener.allowed_origins


def _is_json_content_type(value: str | None) -> bool:
    if value is None:
        return False
    return value.split(";", 1)[0].strip().lower() == "application/json"


def _accepts_terminal_json(value: str | None) -> bool:
    if value is None:
        return False
    accepted = {part.strip().lower() for part in value.split(",")}
    return "application/json" in accepted and "text/event-stream" in accepted
