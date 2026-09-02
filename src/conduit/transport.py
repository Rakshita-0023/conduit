"""Narrow bounded HTTP boundary for Streamable HTTP MCP downstreams."""

from __future__ import annotations

import asyncio
import json
from collections.abc import Mapping
from dataclasses import dataclass
from typing import Any

import httpx

from .errors import ResponseTooLarge, UnsupportedResponse

MCP_VERSION = "2026-07-28"


def protected_header(name: str) -> bool:
    lowered = name.lower()
    return lowered in {"mcp-protocol-version", "mcp-method", "mcp-name", "mcp-session-id"} or lowered.startswith("mcp-param-")


def downstream_headers(headers: Mapping[str, str]) -> dict[str, str]:
    if any(protected_header(name) for name in headers):
        raise ValueError("configured MCP routing headers are not permitted")
    return dict(headers)


@dataclass(frozen=True)
class DownstreamReply:
    status_code: int
    headers: Mapping[str, str]
    body: bytes
    session_id: str | None
    sse: bool


class DownstreamTransport:
    """One shared, redirect-disabled async client with bounded body reads."""

    def __init__(self, timeout: float, *, client: httpx.AsyncClient | None = None) -> None:
        self._client = client or httpx.AsyncClient(follow_redirects=False, timeout=httpx.Timeout(timeout))

    async def close(self) -> None:
        await self._client.aclose()

    async def request(
        self,
        endpoint: str,
        method: str,
        request_id: str,
        params: Mapping[str, Any],
        configured_headers: Mapping[str, str],
        limit: int,
        *,
        tool_name: str | None = None,
        parameter_headers: Mapping[str, str] | None = None,
        timeout: float | None = None,
    ) -> DownstreamReply:
        headers = downstream_headers(configured_headers)
        headers.update(
            {
                "Content-Type": "application/json",
                "Accept": "application/json, text/event-stream",
                "MCP-Protocol-Version": MCP_VERSION,
                "Mcp-Method": method,
            }
        )
        if tool_name is not None:
            headers["Mcp-Name"] = tool_name
        if parameter_headers:
            headers.update(parameter_headers)
        payload = {"jsonrpc": "2.0", "id": request_id, "method": method, "params": params}
        session_id: str | None = None
        try:
            async with self._client.stream("POST", endpoint, headers=headers, json=payload, timeout=timeout) as response:
                session_id = response.headers.get("mcp-session-id")
                content_type = response.headers.get("content-type", "").split(";", 1)[0].strip().lower()
                if content_type == "application/json":
                    body = await read_bounded(response, limit)
                    sse = False
                elif content_type == "text/event-stream":
                    body = await read_terminal_sse(response, request_id, limit)
                    # The body is now one verified terminal JSON-RPC reply.
                    # Keep the legacy flag false so core consumers do not need
                    # a second policy/audit/routing path for SSE transports.
                    sse = False
                else:
                    raise UnsupportedResponse("unsupported downstream content type")
                return DownstreamReply(response.status_code, dict(response.headers), body, session_id, sse)
        except (httpx.HTTPError, ResponseTooLarge, UnsupportedResponse) as exc:
            # A downstream can create a session before its body later proves
            # malformed, oversized, or disconnected. Preserve that ownership
            # information for the dispatcher so its finally block can still
            # send the one bounded cleanup DELETE.
            setattr(exc, "conduit_session_id", session_id)
            raise

    async def cleanup(self, endpoint: str, session_id: str, configured_headers: Mapping[str, str], timeout: float | None) -> None:
        headers = downstream_headers(configured_headers)
        headers["Mcp-Session-Id"] = session_id
        try:
            async with self._client.stream("DELETE", endpoint, headers=headers, timeout=timeout) as response:
                await read_bounded(response, 64 * 1024)
        except asyncio.CancelledError:
            # Shutdown owns the lifetime of an in-flight DELETE.  Swallowing
            # cancellation here would make the active-dispatch lifecycle lie.
            raise
        except (httpx.HTTPError, ResponseTooLarge):
            # A completed invocation must not be replaced by cleanup failure.
            return


async def read_bounded(response: httpx.Response, limit: int) -> bytes:
    """Retain at most limit+1 bytes, detecting overflow without full buffering."""

    retained = bytearray()
    maximum = limit + 1
    chunks = response.aiter_bytes()
    try:
        async for chunk in chunks:
            remaining = maximum - len(retained)
            if remaining <= 0:
                raise ResponseTooLarge("downstream response exceeds byte limit")
            retained.extend(chunk[:remaining])
            if len(chunk) > remaining or len(retained) > limit:
                raise ResponseTooLarge("downstream response exceeds byte limit")
    finally:
        # Overflow deliberately stops consuming the stream. Close the async
        # iterator now rather than relying on eventual garbage collection.
        close_iterator = getattr(chunks, "aclose", None)
        if close_iterator is not None:
            await close_iterator()
    return bytes(retained)


async def read_terminal_sse(response: httpx.Response, request_id: str, limit: int) -> bytes:
    """Reduce one finite SSE message to its correlated terminal JSON-RPC body.

    This is intentionally not an SSE bridge: it accepts exactly one complete
    ``message`` (or default-message) event, consumes the finite response to
    detect ambiguity, and returns the event data as ordinary JSON bytes.
    """

    consumed = 0
    line_buffer = bytearray()
    data_lines: list[bytes] = []
    event_name: bytes | None = None
    terminal: bytes | None = None
    chunks = response.aiter_bytes()
    try:
        async for chunk in chunks:
            consumed += len(chunk)
            if consumed > limit:
                raise ResponseTooLarge("downstream response exceeds byte limit")
            line_buffer.extend(chunk)
            while True:
                newline = line_buffer.find(b"\n")
                if newline < 0:
                    break
                line = bytes(line_buffer[:newline])
                del line_buffer[: newline + 1]
                if line.endswith(b"\r"):
                    line = line[:-1]
                terminal = _consume_sse_line(line, event_name, data_lines, terminal, request_id)
                if not line:
                    event_name = None
                    data_lines.clear()
                elif not line.startswith(b":"):
                    field, _, value = line.partition(b":")
                    if value.startswith(b" "):
                        value = value[1:]
                    if field == b"event":
                        event_name = value
                    elif field == b"data":
                        data_lines.append(value)
        if line_buffer or data_lines:
            raise UnsupportedResponse("truncated downstream SSE response")
        if terminal is None:
            raise UnsupportedResponse("downstream SSE response has no terminal message")
        return terminal
    finally:
        close_iterator = getattr(chunks, "aclose", None)
        if close_iterator is not None:
            await close_iterator()


def _consume_sse_line(
    line: bytes, event_name: bytes | None, data_lines: list[bytes], terminal: bytes | None, request_id: str
) -> bytes | None:
    """Validate a completed SSE event before resetting its parser state."""

    if line or not data_lines:
        return terminal
    if event_name not in (None, b"message"):
        raise UnsupportedResponse("unsupported downstream SSE event")
    if terminal is not None:
        raise UnsupportedResponse("multiple downstream SSE messages")
    candidate = b"\n".join(data_lines)
    result, error = parse_jsonrpc_reply(candidate, request_id)
    if result is None and error is None:
        raise UnsupportedResponse("invalid downstream SSE terminal message")
    return candidate


def parse_jsonrpc_reply(body: bytes, request_id: str) -> tuple[dict[str, Any] | None, dict[str, Any] | None]:
    """Return a correlated result/error; malformed or mismatched envelopes fail."""

    try:
        value = json.loads(body)
    except (UnicodeDecodeError, json.JSONDecodeError):
        return None, None
    if not isinstance(value, dict) or value.get("jsonrpc") != "2.0" or value.get("id") != request_id:
        return None, None
    result = value.get("result")
    error = value.get("error")
    if isinstance(result, dict) and error is None:
        return result, None
    if isinstance(error, dict) and isinstance(error.get("code"), int) and isinstance(error.get("message"), str):
        return None, error
    return None, None
