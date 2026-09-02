"""Narrow bounded HTTP boundary for Streamable HTTP MCP downstreams."""

from __future__ import annotations

import asyncio
import json
from collections.abc import Mapping
from dataclasses import dataclass
from typing import Any

import httpx

from .errors import ResponseTooLarge

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
                content_type = response.headers.get("content-type", "").lower()
                body = await read_bounded(response, limit)
                return DownstreamReply(response.status_code, dict(response.headers), body, session_id, content_type.startswith("text/event-stream"))
        except (httpx.HTTPError, ResponseTooLarge) as exc:
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


def parse_jsonrpc_reply(body: bytes, request_id: str) -> tuple[dict[str, Any] | None, dict[str, Any] | None]:
    """Return a correlated result/error; malformed or mismatched envelopes fail."""

    try:
        value = json.loads(body)
    except (UnicodeDecodeError, json.JSONDecodeError):
        return None, None
    if not isinstance(value, dict) or value.get("jsonrpc") != "2.0" or str(value.get("id")) != request_id:
        return None, None
    result = value.get("result")
    error = value.get("error")
    if isinstance(result, dict) and error is None:
        return result, None
    if isinstance(error, dict) and isinstance(error.get("code"), int) and isinstance(error.get("message"), str):
        return None, error
    return None, None
