"""Real HTTP integration coverage for downstream session and streaming edges."""

from __future__ import annotations

import asyncio
import json
import socket
import threading
import time
from contextlib import contextmanager
from dataclasses import replace
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Iterator

import pytest
import yaml
from starlette.testclient import TestClient

from conduit.app import Runtime, create_app
from conduit.config import AuditConfig, parse_config
from conduit.errors import TOOL_OUTCOME_UNKNOWN, TOOL_RESPONSE_UNSUPPORTED, GatewayError
from conduit.protocol import MCP_PROTOCOL_VERSION


class _StatefulDownstream:
    """A real HTTP downstream that records per-invocation session ownership."""

    def __init__(self, name: str) -> None:
        self.name = name
        self.mode = "normal"
        self.requests: list[tuple[str, dict[str, str], dict[str, Any]]] = []
        self.cleaned: list[str] = []
        self._sequence = 0
        self.delay_started = threading.Event()
        self.release_delay = threading.Event()
        owner = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *_: object) -> None:  # pragma: no cover - silence fixture server
                return

            def do_DELETE(self) -> None:  # noqa: N802
                owner.cleaned.append(self.headers.get("Mcp-Session-Id", ""))
                self.send_response(204)
                self.send_header("Content-Length", "0")
                self.end_headers()

            def do_POST(self) -> None:  # noqa: N802
                size = int(self.headers.get("content-length", "0"))
                payload = json.loads(self.rfile.read(size))
                headers = dict(self.headers)
                owner.requests.append(("POST", headers, payload))
                method = payload["method"]
                if method == "server/discover":
                    self._json(payload["id"], {"supportedVersions": [MCP_PROTOCOL_VERSION]})
                    return
                if method == "tools/list":
                    self._json(payload["id"], {"tools": [{"name": "work", "inputSchema": {"type": "object"}}]})
                    return
                assert method == "tools/call"
                # Invocation-owned sessions must never be fed to an unrelated
                # call. A downstream restart/invalidation therefore has no
                # stale token to reject on Conduit's next invocation.
                assert "mcp-session-id" not in {key.lower() for key in headers}
                owner._sequence += 1
                session = f"{owner.name}-session-{owner._sequence}"
                if owner.mode == "delay":
                    owner.delay_started.set()
                    assert owner.release_delay.wait(timeout=5)
                if owner.mode in {"sse", "malformed_sse"}:
                    body = b"event: message\ndata: {not-json}\n\n" if owner.mode == "malformed_sse" else b"event: message\ndata: progress\n\n"
                    self.send_response(200)
                    self.send_header("Content-Type", "text/event-stream")
                    self.send_header("Mcp-Session-Id", session)
                    self.send_header("Content-Length", str(len(body)))
                    self.end_headers()
                    self.wfile.write(body)
                    return
                if owner.mode == "oversized_stream":
                    body = b"event: message\ndata: " + b"x" * 4096 + b"\n\n"
                    self.send_response(200)
                    self.send_header("Content-Type", "text/event-stream")
                    self.send_header("Mcp-Session-Id", session)
                    self.send_header("Content-Length", str(len(body)))
                    self.end_headers()
                    self.wfile.write(body)
                    return
                if owner.mode == "disconnect":
                    body = json.dumps({"jsonrpc": "2.0", "id": payload["id"], "result": {"content": []}}).encode()
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Mcp-Session-Id", session)
                    self.send_header("Content-Length", str(len(body) + 20))
                    self.end_headers()
                    self.wfile.write(body[:8])
                    self.wfile.flush()
                    self.connection.shutdown(socket.SHUT_RDWR)
                    self.close_connection = True
                    return
                self._json(payload["id"], {"content": [], "structuredContent": {"server": owner.name, "session": session}}, session=session)

            def _json(self, request_id: str, result: dict[str, Any], *, session: str | None = None) -> None:
                body = json.dumps({"jsonrpc": "2.0", "id": request_id, "result": result}).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                if session is not None:
                    self.send_header("Mcp-Session-Id", session)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)

    @property
    def url(self) -> str:
        return f"http://127.0.0.1:{self.server.server_port}/mcp"

    def start(self) -> None:
        self.thread.start()

    def close(self) -> None:
        self.release_delay.set()
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)
        assert not self.thread.is_alive()


@contextmanager
def _servers() -> Iterator[tuple[_StatefulDownstream, _StatefulDownstream]]:
    a = _StatefulDownstream("alpha")
    b = _StatefulDownstream("bravo")
    a.start()
    b.start()
    try:
        yield a, b
    finally:
        a.close()
        b.close()


def _config(tmp_path: Path, a: _StatefulDownstream, b: _StatefulDownstream, *, response_limit: int = 8192):
    return parse_config(
        yaml.safe_load(
            f"""
listener: {{address: 127.0.0.1:8080, allowed_origins: []}}
audit: {{path: {tmp_path / 'audit.jsonl'}}}
policy: {{allow: [alpha.*, bravo.*], deny: []}}
limits:
  max_pages_per_downstream: 4
  max_tools_per_downstream: 10
  max_downstream_catalog_bytes: 65536
  max_aggregate_tools: 10
  max_aggregate_response_bytes: 65536
  max_tool_response_bytes: {response_limit}
  catalog_refresh_interval: 50ms
  request_timeout: 1s
  tool_call_timeout: 1s
downstreams:
  - id: alpha
    url: {a.url}
    headers: {{X-Owner: alpha-secret}}
  - id: bravo
    url: {b.url}
    headers: {{X-Owner: bravo-secret}}
"""
        )
    )


def _headers(method: str, name: str | None = None) -> dict[str, str]:
    headers = {
        "Content-Type": "application/json",
        "Accept": "application/json, text/event-stream",
        "MCP-Protocol-Version": MCP_PROTOCOL_VERSION,
        "Mcp-Method": method,
    }
    if name is not None:
        headers["Mcp-Name"] = name
    return headers


def _body(method: str, request_id: int, name: str | None = None) -> dict[str, Any]:
    params: dict[str, Any] = {"_meta": {"io.modelcontextprotocol/protocolVersion": MCP_PROTOCOL_VERSION, "io.modelcontextprotocol/clientCapabilities": {}}}
    if name is not None:
        params.update({"name": name, "arguments": {}})
    return {"jsonrpc": "2.0", "id": request_id, "method": method, "params": params}


def _wait_ready(client: TestClient) -> None:
    deadline = time.monotonic() + 3
    while time.monotonic() < deadline:
        if client.get("/healthz").json()["ready"]:
            return
        time.sleep(0.01)
    pytest.fail("gateway did not become ready")


def _call(client: TestClient, request_id: int, name: str) -> Any:
    return client.post("/mcp", headers=_headers("tools/call", name), json=_body("tools/call", request_id, name))


def test_sessions_are_invocation_owned_isolated_and_recover_after_downstream_reset(tmp_path: Path) -> None:
    with _servers() as (a, b):
        with TestClient(create_app(_config(tmp_path, a, b), build_version="test")) as client:
            _wait_ready(client)
            assert _call(client, 1, "alpha.work").json()["result"]["structuredContent"]["server"] == "alpha"
            assert _call(client, 2, "bravo.work").json()["result"]["structuredContent"]["server"] == "bravo"
            # A new alpha call represents a downstream restart/session loss:
            # no old alpha nor any bravo identifier is presented to it.
            assert _call(client, 3, "alpha.work").status_code == 200

            assert a.cleaned == ["alpha-session-1", "alpha-session-2"]
            assert b.cleaned == ["bravo-session-1"]
            for server in (a, b):
                calls = [item for item in server.requests if item[2]["method"] == "tools/call"]
                assert all("mcp-session-id" not in {key.lower() for key in headers} for _, headers, _ in calls)
            assert client.get("/healthz").json()["ready"] is True


@pytest.mark.asyncio
async def test_concurrent_calls_keep_independent_downstream_session_ownership(tmp_path: Path) -> None:
    with _servers() as (a, b):
        runtime = Runtime(_config(tmp_path, a, b), "test")
        await runtime.start()
        try:
            deadline = time.monotonic() + 3
            while not runtime.health.snapshot()["ready"] and time.monotonic() < deadline:
                await asyncio.sleep(0.01)
            replies = await asyncio.gather(
                *(runtime.dispatcher.execute("alpha.work" if index % 2 == 0 else "bravo.work", {}, None, None) for index in range(16))
            )
            assert {reply["result"]["structuredContent"]["server"] for reply in replies} == {"alpha", "bravo"}
            assert len(a.cleaned) == 8 and len(b.cleaned) == 8
            assert all(session.startswith("alpha-session-") for session in a.cleaned)
            assert all(session.startswith("bravo-session-") for session in b.cleaned)
        finally:
            await runtime.close()


@pytest.mark.parametrize(
    ("mode", "limit", "code"),
    [("sse", 8192, TOOL_RESPONSE_UNSUPPORTED), ("malformed_sse", 8192, TOOL_RESPONSE_UNSUPPORTED), ("oversized_stream", 512, TOOL_OUTCOME_UNKNOWN), ("disconnect", 8192, TOOL_OUTCOME_UNKNOWN)],
)
def test_stream_failures_are_terminal_one_shot_cleaned_and_recoverable(tmp_path: Path, mode: str, limit: int, code: int) -> None:
    with _servers() as (a, b):
        a.mode = mode
        with TestClient(create_app(_config(tmp_path, a, b, response_limit=limit), build_version="test")) as client:
            _wait_ready(client)
            failed = _call(client, 1, "alpha.work")
            assert failed.status_code == 200
            assert failed.json()["error"]["code"] == code
            assert len([item for item in a.requests if item[2]["method"] == "tools/call"]) == 1
            # The session id arrived before every failure, including a broken
            # body stream; cleanup therefore remains tied to this invocation.
            assert a.cleaned == ["alpha-session-1"]

            a.mode = "normal"
            recovered = _call(client, 2, "alpha.work")
            assert recovered.json()["result"]["structuredContent"]["server"] == "alpha"
            assert _call(client, 3, "bravo.work").status_code == 200
            assert a.cleaned == ["alpha-session-1", "alpha-session-2"]


def test_malformed_or_cross_namespace_public_names_never_reach_a_downstream(tmp_path: Path) -> None:
    with _servers() as (a, b):
        with TestClient(create_app(_config(tmp_path, a, b), build_version="test")) as client:
            _wait_ready(client)
            for request_id, name in enumerate(("alpha..work", "alpha/work", "alpha.work.", "bravo.alpha-work", "github.delete"), start=1):
                rejected = _call(client, request_id, name)
                assert rejected.json()["error"]["code"] == -32010
            assert not [item for item in a.requests if item[2]["method"] == "tools/call"]
            assert not [item for item in b.requests if item[2]["method"] == "tools/call"]


@pytest.mark.asyncio
async def test_runtime_shutdown_cancels_active_real_http_call_without_hanging(tmp_path: Path) -> None:
    with _servers() as (a, b):
        a.mode = "delay"
        config = _config(tmp_path, a, b)
        runtime = Runtime(replace(config, audit=AuditConfig(str(tmp_path / "shutdown-audit.jsonl"))), "test")
        await runtime.start()
        try:
            deadline = time.monotonic() + 3
            while not runtime.health.snapshot()["ready"] and time.monotonic() < deadline:
                await asyncio.sleep(0.01)
            assert runtime.health.snapshot()["ready"]
            active = asyncio.create_task(runtime.dispatcher.execute("alpha.work", {}, None, None))
            await asyncio.to_thread(a.delay_started.wait, 2)
            await runtime.close()
            with pytest.raises(GatewayError) as error:
                await active
            assert error.value.code == TOOL_OUTCOME_UNKNOWN
            assert not runtime.health.snapshot()["live"]
            assert "tool_call_unknown_after_dispatch" in (tmp_path / "shutdown-audit.jsonl").read_text(encoding="utf-8")
        finally:
            if runtime.audit.available:
                await runtime.close()
