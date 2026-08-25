from __future__ import annotations

import httpx
import pytest

from conduit.errors import ResponseTooLarge
from conduit.transport import DownstreamTransport, downstream_headers, parse_jsonrpc_reply


@pytest.mark.asyncio
async def test_transport_sends_only_required_downstream_headers_and_never_follows_redirects() -> None:
    observed: list[httpx.Request] = []

    async def handler(request: httpx.Request) -> httpx.Response:
        observed.append(request)
        return httpx.Response(200, json={"jsonrpc": "2.0", "id": "id", "result": {"content": []}})

    client = httpx.AsyncClient(transport=httpx.MockTransport(handler), follow_redirects=False)
    transport = DownstreamTransport(1, client=client)
    reply = await transport.request("https://downstream.invalid/mcp", "tools/call", "id", {"name": "x"}, {"Authorization": "configured-only"}, 1024, tool_name="real")
    assert reply.status_code == 200
    assert observed[0].headers["authorization"] == "configured-only"
    assert observed[0].headers["mcp-name"] == "real"
    await transport.close()


@pytest.mark.asyncio
async def test_non_2xx_correlated_error_preserves_code_message_and_data() -> None:
    async def handler(_: httpx.Request) -> httpx.Response:
        return httpx.Response(400, json={"jsonrpc": "2.0", "id": "id", "error": {"code": -32602, "message": "bad parameters", "data": {"field": "x"}}})

    transport = DownstreamTransport(1, client=httpx.AsyncClient(transport=httpx.MockTransport(handler)))
    reply = await transport.request("https://downstream.invalid/mcp", "tools/call", "id", {"name": "x"}, {}, 1024, tool_name="real")
    result, error = parse_jsonrpc_reply(reply.body, "id")
    assert result is None
    assert error == {"code": -32602, "message": "bad parameters", "data": {"field": "x"}}
    await transport.close()


@pytest.mark.asyncio
async def test_response_bound_rejects_one_byte_over_and_accepts_exact() -> None:
    exact = b"x" * 8

    async def handler(_: httpx.Request) -> httpx.Response:
        return httpx.Response(200, content=exact)

    transport = DownstreamTransport(1, client=httpx.AsyncClient(transport=httpx.MockTransport(handler)))
    assert (await transport.request("https://downstream.invalid/mcp", "tools/list", "id", {}, {}, 8)).body == exact
    with pytest.raises(ResponseTooLarge):
        await transport.request("https://downstream.invalid/mcp", "tools/list", "id", {}, {}, 7)
    await transport.close()


def test_transport_rejects_protected_headers_and_malformed_replies() -> None:
    with pytest.raises(ValueError):
        downstream_headers({"mcp-session-id": "fixed"})
    assert parse_jsonrpc_reply(b"not json", "id") == (None, None)
    assert parse_jsonrpc_reply(b'{"jsonrpc":"2.0","id":"other","result":{}}', "id") == (None, None)
    assert parse_jsonrpc_reply(b'{"jsonrpc":"2.0","id":"id","error":{"code":"bad"}}', "id") == (None, None)
