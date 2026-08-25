"""Downstream server/discover and bounded tools/list refresh operations."""

from __future__ import annotations

from collections.abc import Mapping
from typing import Any

from .config import DownstreamConfig, LimitsConfig
from .transport import MCP_VERSION, DownstreamTransport, parse_jsonrpc_reply


class CatalogError(RuntimeError):
    """A downstream catalog is not safe to publish."""


async def refresh_catalog(transport: DownstreamTransport, downstream: DownstreamConfig, limits: LimitsConfig, request_id: str) -> list[Mapping[str, Any]]:
    discovery = await transport.request(
        downstream.url,
        "server/discover",
        request_id + "-discover",
        _meta(),
        downstream.headers,
        limits.max_downstream_catalog_bytes,
        timeout=limits.request_timeout_seconds,
    )
    discovered, error = parse_jsonrpc_reply(discovery.body, request_id + "-discover")
    if discovery.status_code < 200 or discovery.status_code >= 300 or error is not None or discovered is None or MCP_VERSION not in discovered.get("supportedVersions", []):
        raise CatalogError("downstream does not support MCP 2026-07-28")
    tools: list[Mapping[str, Any]] = []
    cursor: str | None = None
    seen: set[str] = set()
    for page in range(1, limits.max_pages_per_downstream + 1):
        params = _meta()
        if cursor:
            params["cursor"] = cursor
        reply = await transport.request(
            downstream.url,
            "tools/list",
            f"{request_id}-list-{page}",
            params,
            downstream.headers,
            limits.max_downstream_catalog_bytes,
            timeout=limits.request_timeout_seconds,
        )
        result, error = parse_jsonrpc_reply(reply.body, f"{request_id}-list-{page}")
        page_tools = result.get("tools") if result is not None else None
        if reply.status_code < 200 or reply.status_code >= 300 or error is not None or not isinstance(page_tools, list):
            raise CatalogError("downstream tools/list failed")
        for tool in page_tools:
            if not isinstance(tool, Mapping) or not isinstance(tool.get("name"), str) or not tool["name"] or not isinstance(tool.get("inputSchema"), Mapping):
                raise CatalogError("malformed tool definition")
            tools.append(dict(tool))
            if len(tools) > limits.max_tools_per_downstream:
                raise CatalogError("catalog tool limit exceeded")
        cursor = result.get("nextCursor") if result is not None else None
        if not cursor:
            return tools
        if not isinstance(cursor, str) or cursor in seen:
            raise CatalogError("repeated opaque cursor")
        seen.add(cursor)
    raise CatalogError("catalog page limit exceeded")


def _meta() -> dict[str, Any]:
    return {
        "_meta": {
            "io.modelcontextprotocol/protocolVersion": MCP_VERSION,
            "io.modelcontextprotocol/clientCapabilities": {},
        }
    }
