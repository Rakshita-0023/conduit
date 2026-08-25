"""Authorized, one-shot downstream ``tools/call`` execution."""

from __future__ import annotations

import asyncio
import base64
import secrets
import time
from collections.abc import Mapping
from typing import Any

import httpx

from .audit import AuditLog
from .config import Config
from .errors import (
    AUDIT_UNAVAILABLE,
    TOOL_DISPATCH_FAILED,
    TOOL_OUTCOME_UNKNOWN,
    TOOL_RESPONSE_UNSUPPORTED,
    TOOL_UNAVAILABLE,
    AuditUnavailable,
    GatewayError,
    ResponseTooLarge,
    UnsupportedResponse,
)
from .headers import HeaderError, generate_headers
from .health import HealthState
from .registry import PreparedRoute, Registry, RouteChanged, RouteDenied, RouteMissing
from .transport import DownstreamTransport, parse_jsonrpc_reply


class Dispatcher:
    """Own active calls, authorization, terminal audit, and shutdown admission."""

    def __init__(self, config: Config, registry: Registry, audit: AuditLog, health: HealthState, transport: DownstreamTransport, build_version: str) -> None:
        self._config = config
        self._registry = registry
        self._audit = audit
        self._health = health
        self._transport = transport
        self._build_version = build_version
        self._servers = {server.id: server for server in config.downstreams}
        self._closing = False
        self._admission = asyncio.Lock()
        self._active: set[asyncio.Task[object]] = set()
        self._empty = asyncio.Event()
        self._empty.set()

    async def execute(self, public_name: str, arguments: Mapping[str, Any], input_responses: object | None, request_state: object | None) -> dict[str, Any]:
        task = asyncio.current_task()
        if task is None or not await self._admit(task):
            raise GatewayError(TOOL_UNAVAILABLE, "tool unavailable")
        try:
            return await self._execute(public_name, arguments, input_responses, request_state)
        finally:
            await self._release(task)

    async def begin_shutdown(self) -> None:
        async with self._admission:
            self._closing = True

    async def cancel_active(self) -> None:
        await self.begin_shutdown()
        current = asyncio.current_task()
        for task in tuple(self._active):
            if task is not current:
                task.cancel()

    async def wait(self) -> None:
        await self._empty.wait()

    async def _admit(self, task: asyncio.Task[object]) -> bool:
        async with self._admission:
            if self._closing:
                return False
            self._active.add(task)
            self._empty.clear()
            return True

    async def _release(self, task: asyncio.Task[object]) -> None:
        async with self._admission:
            self._active.discard(task)
            if not self._active:
                self._empty.set()

    async def _execute(self, public_name: str, arguments: Mapping[str, Any], input_responses: object | None, request_state: object | None) -> dict[str, Any]:
        call_id = base64.urlsafe_b64encode(secrets.token_bytes(16)).decode().rstrip("=")
        if not self._audit.available:
            self._health.set_audit(False)
            raise GatewayError(AUDIT_UNAVAILABLE, "audit unavailable")
        if not self._health.snapshot()["ready"]:
            raise GatewayError(TOOL_UNAVAILABLE, "tool unavailable")
        prepared = await self._prepare_and_authorize(call_id, public_name, arguments)
        downstream = self._servers.get(prepared.route.server_id)
        if downstream is None:
            await self._terminal(call_id, prepared, "tool_call_downstream_error", "missing_server", 0)
            raise GatewayError(TOOL_DISPATCH_FAILED, "tool dispatch failed")
        params: dict[str, Any] = {
            "name": prepared.route.downstream_tool_name,
            "arguments": dict(arguments),
            "_meta": {"io.modelcontextprotocol/protocolVersion": "2026-07-28", "io.modelcontextprotocol/clientInfo": {"name": "conduit", "version": self._build_version}, "io.modelcontextprotocol/clientCapabilities": {}},
        }
        if input_responses is not None:
            params["inputResponses"] = input_responses
        if request_state is not None:
            params["requestState"] = request_state
        started = False
        session_id: str | None = None
        begin = time.monotonic()
        try:
            async with asyncio.timeout(self._config.limits.tool_call_timeout_seconds):
                started = True
                reply = await self._transport.request(
                    downstream.url,
                    "tools/call",
                    call_id,
                    params,
                    downstream.headers,
                    self._config.limits.max_tool_response_bytes,
                    tool_name=prepared.route.downstream_tool_name,
                    parameter_headers=generate_headers(prepared.tool, arguments),
                    timeout=self._config.limits.tool_call_timeout_seconds,
                )
                session_id = reply.session_id
                if reply.sse:
                    raise UnsupportedResponse("downstream SSE is unsupported")
                result, wire_error = parse_jsonrpc_reply(reply.body, call_id)
                if reply.status_code < 200 or reply.status_code >= 300:
                    if wire_error is not None:
                        await self._terminal(call_id, prepared, "tool_call_downstream_error", "downstream_jsonrpc_error", _ms(begin))
                        return {"error": wire_error}
                    raise RuntimeError("non-2xx downstream response")
                if wire_error is not None:
                    await self._terminal(call_id, prepared, "tool_call_downstream_error", "downstream_jsonrpc_error", _ms(begin))
                    return {"error": wire_error}
                if result is None:
                    raise RuntimeError("invalid downstream response")
                _validate_result(result)
                await self._terminal(call_id, prepared, "tool_call_completed", "completed", _ms(begin))
                return {"result": result}
        except UnsupportedResponse:
            await self._terminal(call_id, prepared, "tool_call_downstream_error", "unsupported_response", _ms(begin))
            raise GatewayError(TOOL_RESPONSE_UNSUPPORTED, "tool response unsupported")
        except asyncio.CancelledError:
            if started:
                await self._terminal(call_id, prepared, "tool_call_unknown_after_dispatch", "unknown", _ms(begin))
                raise GatewayError(TOOL_OUTCOME_UNKNOWN, "tool outcome unknown")
            await self._terminal(call_id, prepared, "tool_call_downstream_error", "cancelled_before_dispatch", _ms(begin))
            raise GatewayError(TOOL_DISPATCH_FAILED, "tool dispatch failed")
        except (httpx.HTTPError, ResponseTooLarge, TimeoutError, RuntimeError):
            if started:
                await self._terminal(call_id, prepared, "tool_call_unknown_after_dispatch", "unknown", _ms(begin))
                raise GatewayError(TOOL_OUTCOME_UNKNOWN, "tool outcome unknown")
            await self._terminal(call_id, prepared, "tool_call_downstream_error", "local_failure", _ms(begin))
            raise GatewayError(TOOL_DISPATCH_FAILED, "tool dispatch failed")
        finally:
            if session_id:
                await self._transport.cleanup(downstream.url, session_id, downstream.headers, self._config.limits.tool_call_timeout_seconds)

    async def _prepare_and_authorize(self, call_id: str, name: str, arguments: Mapping[str, Any]) -> PreparedRoute:
        for _ in range(3):
            try:
                prepared = await self._registry.prepare(name)
                generate_headers(prepared.tool, arguments)
                await self._registry.authorize(prepared, lambda: self._authorized(call_id, prepared))
                return prepared
            except RouteChanged:
                continue
            except (RouteMissing, RouteDenied, HeaderError):
                await self._deny(call_id, name, "unavailable")
                raise GatewayError(TOOL_UNAVAILABLE, "tool unavailable")
            except AuditUnavailable:
                self._health.set_audit(False)
                raise GatewayError(AUDIT_UNAVAILABLE, "audit unavailable")
        await self._deny(call_id, name, "route_changing")
        raise GatewayError(TOOL_UNAVAILABLE, "tool unavailable")

    async def _authorized(self, call_id: str, prepared: PreparedRoute) -> None:
        await self._audit.append({"event": "tool_call_authorized", "call_id": call_id, "public_tool": prepared.route.public_name, "server_id": prepared.route.server_id, "downstream_tool_name": prepared.route.downstream_tool_name, "registry_generation": prepared.generation, "policy_digest": prepared.policy_digest})

    async def _deny(self, call_id: str, name: str, outcome: str) -> None:
        try:
            await self._audit.append({"event": "tool_call_denied", "call_id": call_id, "public_tool": name, "outcome": outcome})
        except AuditUnavailable:
            self._health.set_audit(False)
            raise

    async def _terminal(self, call_id: str, prepared: PreparedRoute, event: str, outcome: str, duration_ms: int) -> None:
        try:
            await asyncio.shield(self._audit.append({"event": event, "call_id": call_id, "public_tool": prepared.route.public_name, "server_id": prepared.route.server_id, "downstream_tool_name": prepared.route.downstream_tool_name, "registry_generation": prepared.generation, "policy_digest": prepared.policy_digest, "outcome": outcome, "duration_ms": duration_ms}))
        except AuditUnavailable:
            self._health.set_audit(False)


def _ms(begin: float) -> int:
    return int((time.monotonic() - begin) * 1000)


def _validate_result(result: Mapping[str, Any]) -> None:
    result_type = result.get("resultType")
    if result_type not in (None, "", "complete", "input_required"):
        raise RuntimeError("invalid result type")
    if result_type == "input_required":
        requests = result.get("inputRequests")
        if requests not in (None, {}) or not isinstance(result.get("requestState"), str):
            raise UnsupportedResponse("input required unsupported")
