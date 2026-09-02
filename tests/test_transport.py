import asyncio
from collections.abc import AsyncIterator

import httpx
import pytest

from conduit.errors import ResponseTooLarge, UnsupportedResponse
from conduit.transport import DownstreamTransport, downstream_headers, parse_jsonrpc_reply


class _Chunks(httpx.AsyncByteStream):
    def __init__(self, chunks: list[bytes]) -> None:
        self._chunks = chunks
        self.closed = False

    async def __aiter__(self) -> AsyncIterator[bytes]:
        for chunk in self._chunks:
            yield chunk

    async def aclose(self) -> None:
        self.closed = True


class _BlockingChunks(_Chunks):
    def __init__(self) -> None:
        super().__init__([])
        self.started = asyncio.Event()
        self.release = asyncio.Event()

    async def __aiter__(self) -> AsyncIterator[bytes]:
        self.started.set()
        await self.release.wait()
        if False:  # pragma: no cover - gives the method its async-generator type
            yield b""


def _sse(chunks: list[bytes]) -> tuple[httpx.Response, _Chunks]:
    stream = _Chunks(chunks)
    return httpx.Response(200, headers={"Content-Type": "text/event-stream; charset=utf-8"}, stream=stream), stream


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
        return httpx.Response(200, content=exact, headers={"Content-Type": "application/json"})

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
    assert parse_jsonrpc_reply(b'{"jsonrpc":"2.0","id":1,"result":{}}', "1") == (None, None)
    assert parse_jsonrpc_reply(b'{"jsonrpc":"2.0","id":"id","error":{"code":"bad"}}', "id") == (None, None)


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "chunks",
    [
        [b": comment\r\n\r\nevent: mes", b"sage\r\ndata: {\"jsonrpc\":\"2.0\",\r\ndata: \"id\":\"id\",\"result\":{}}\r\n\r\n"],
        [b"\n", b"data: {\"jsonrpc\":\"2.0\",\"id\":\"id\",\"result\":{}}\n\n"],
    ],
)
async def test_transport_reduces_finite_terminal_sse_with_chunk_and_line_variants(chunks: list[bytes]) -> None:
    response, stream = _sse(chunks)

    async def handler(_: httpx.Request) -> httpx.Response:
        return response

    transport = DownstreamTransport(1, client=httpx.AsyncClient(transport=httpx.MockTransport(handler)))
    reply = await transport.request("https://downstream.invalid/mcp", "tools/list", "id", {}, {}, 1024)
    assert reply.sse is False
    assert parse_jsonrpc_reply(reply.body, "id") == ({}, None)
    assert stream.closed
    await transport.close()


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "body",
    [
        b"event: message\ndata: not-json\n\n",
        b"event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"other\",\"result\":{}}\n\n",
        b"event: progress\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"id\",\"result\":{}}\n\n",
        b"event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"id\",\"result\":{}}\n\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"id\",\"result\":{}}\n\n",
        b"event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"id\",\"result\":{}}",
        b": comment only\n\n",
    ],
)
async def test_transport_rejects_ambiguous_or_malformed_terminal_sse(body: bytes) -> None:
    response, stream = _sse([body])

    async def handler(_: httpx.Request) -> httpx.Response:
        return response

    transport = DownstreamTransport(1, client=httpx.AsyncClient(transport=httpx.MockTransport(handler)))
    with pytest.raises(UnsupportedResponse):
        await transport.request("https://downstream.invalid/mcp", "tools/list", "id", {}, {}, 4096)
    assert stream.closed
    await transport.close()


@pytest.mark.asyncio
async def test_transport_rejects_wrong_content_type_and_bounds_sse_before_buffering() -> None:
    async def wrong_type(_: httpx.Request) -> httpx.Response:
        return httpx.Response(200, content=b"{}", headers={"Content-Type": "text/plain"})

    transport = DownstreamTransport(1, client=httpx.AsyncClient(transport=httpx.MockTransport(wrong_type)))
    with pytest.raises(UnsupportedResponse):
        await transport.request("https://downstream.invalid/mcp", "tools/list", "id", {}, {}, 8)
    await transport.close()

    response, stream = _sse([b"event: message\ndata: " + b"x" * 64])

    async def oversized(_: httpx.Request) -> httpx.Response:
        return response

    transport = DownstreamTransport(1, client=httpx.AsyncClient(transport=httpx.MockTransport(oversized)))
    with pytest.raises(ResponseTooLarge):
        await transport.request("https://downstream.invalid/mcp", "tools/list", "id", {}, {}, 16)
    assert stream.closed
    await transport.close()


@pytest.mark.asyncio
async def test_cancelling_sse_read_closes_its_iterator_without_replaying() -> None:
    stream = _BlockingChunks()

    async def handler(_: httpx.Request) -> httpx.Response:
        return httpx.Response(200, headers={"Content-Type": "text/event-stream"}, stream=stream)

    transport = DownstreamTransport(1, client=httpx.AsyncClient(transport=httpx.MockTransport(handler)))
    task = asyncio.create_task(transport.request("https://downstream.invalid/mcp", "tools/call", "id", {}, {}, 1024))
    await stream.started.wait()
    task.cancel()
    with pytest.raises(asyncio.CancelledError):
        await task
    assert stream.closed
    await transport.close()


@pytest.mark.asyncio
async def test_sse_terminal_deadline_rejects_a_drip_stream_without_a_terminal_message() -> None:
    """A peer cannot hold a catalog refresh forever by sending keepalives."""

    stream = _BlockingChunks()

    async def handler(_: httpx.Request) -> httpx.Response:
        return httpx.Response(200, headers={"Content-Type": "text/event-stream"}, stream=stream)

    transport = DownstreamTransport(1, client=httpx.AsyncClient(transport=httpx.MockTransport(handler)))
    with pytest.raises(TimeoutError):
        await transport.request("https://downstream.invalid/mcp", "tools/list", "id", {}, {}, 1024, timeout=0.01)
    assert stream.closed
    await transport.close()
