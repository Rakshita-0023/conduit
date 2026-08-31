from __future__ import annotations

import json
from dataclasses import replace

import pytest

from conduit.catalog import CatalogError, refresh_catalog
from conduit.transport import DownstreamReply


class _CatalogTransport:
    def __init__(self, replies: list[DownstreamReply]) -> None:
        self.replies = replies
        self.methods: list[str] = []

    async def request(self, _: str, method: str, request_id: str, *__: object, **___: object) -> DownstreamReply:
        self.methods.append(method)
        reply = self.replies.pop(0)
        if reply.body == b"raw":
            return reply
        payload = json.loads(reply.body)
        payload["id"] = request_id
        return DownstreamReply(reply.status_code, reply.headers, json.dumps(payload).encode(), reply.session_id, reply.sse)


def _reply(result: object, status: int = 200) -> DownstreamReply:
    return DownstreamReply(status, {}, json.dumps({"jsonrpc": "2.0", "id": "x", "result": result}).encode(), None, False)


@pytest.mark.asyncio
async def test_catalog_discovers_pages_and_rejects_unsafe_catalogs(config) -> None:
    transport = _CatalogTransport(
        [
            _reply({"supportedVersions": ["2026-07-28"]}),
            _reply({"tools": [{"name": "one", "inputSchema": {"type": "object"}}], "nextCursor": "next"}),
            _reply({"tools": [{"name": "two", "inputSchema": {"type": "object"}}]}),
        ]
    )
    tools = await refresh_catalog(transport, config.downstreams[0], config.limits, "call")
    assert [tool["name"] for tool in tools] == ["one", "two"]
    assert transport.methods == ["server/discover", "tools/list", "tools/list"]

    failed = _CatalogTransport([_reply({"supportedVersions": []})])
    with pytest.raises(CatalogError):
        await refresh_catalog(failed, config.downstreams[0], config.limits, "call")

    malformed = _CatalogTransport([_reply({"supportedVersions": ["2026-07-28"]}), _reply({"tools": [{"name": "missing-schema"}]})])
    with pytest.raises(CatalogError):
        await refresh_catalog(malformed, config.downstreams[0], config.limits, "call")


@pytest.mark.asyncio
async def test_catalog_rejects_malformed_json_repeated_cursor_and_page_cap(config) -> None:
    malformed_json = _CatalogTransport([_reply({"supportedVersions": ["2026-07-28"]}), DownstreamReply(200, {}, b"raw", None, False)])
    with pytest.raises(CatalogError):
        await refresh_catalog(malformed_json, config.downstreams[0], config.limits, "call")

    repeated_cursor = _CatalogTransport(
        [
            _reply({"supportedVersions": ["2026-07-28"]}),
            _reply({"tools": [], "nextCursor": "again"}),
            _reply({"tools": [], "nextCursor": "again"}),
        ]
    )
    with pytest.raises(CatalogError, match="repeated opaque cursor"):
        await refresh_catalog(repeated_cursor, config.downstreams[0], config.limits, "call")

    one_page = replace(config.limits, max_pages_per_downstream=1)
    excessive_pages = _CatalogTransport([_reply({"supportedVersions": ["2026-07-28"]}), _reply({"tools": [], "nextCursor": "next"})])
    with pytest.raises(CatalogError, match="page limit"):
        await refresh_catalog(excessive_pages, config.downstreams[0], one_page, "call")
