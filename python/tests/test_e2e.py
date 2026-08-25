"""Local public-boundary tests for the Python gateway implementation."""

from __future__ import annotations

import json
import threading
import time
from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Iterator

import pytest
import yaml
from starlette.testclient import TestClient

from conduit.app import create_app
from conduit.config import parse_config
from conduit.protocol import MCP_PROTOCOL_VERSION


class _Downstream:
    def __init__(self, tools: list[dict[str, Any]], result: dict[str, Any], audit_path: Path) -> None:
        self.tools = tools
        self.result = result
        self.audit_path = audit_path
        self.requests: list[tuple[str, dict[str, str], dict[str, Any]]] = []
        self.authorized_before_call = False
        owner = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_: object) -> None:  # pragma: no cover - silence local test server
                return

            def do_DELETE(self) -> None:  # noqa: N802
                owner.requests.append(("DELETE", dict(self.headers), {}))
                self.send_response(204)
                self.end_headers()

            def do_POST(self) -> None:  # noqa: N802
                size = int(self.headers.get("content-length", "0"))
                payload = json.loads(self.rfile.read(size))
                owner.requests.append(("POST", dict(self.headers), payload))
                method = payload["method"]
                if method == "tools/call":
                    owner.authorized_before_call = "tool_call_authorized" in owner.audit_path.read_text(encoding="utf-8")
                    response: dict[str, Any] = {"jsonrpc": "2.0", "id": payload["id"], "result": owner.result}
                    self.send_response(200)
                    self.send_header("Mcp-Session-Id", "owned-session")
                elif method == "server/discover":
                    response = {"jsonrpc": "2.0", "id": payload["id"], "result": {"supportedVersions": [MCP_PROTOCOL_VERSION]}}
                    self.send_response(200)
                elif method == "tools/list":
                    response = {"jsonrpc": "2.0", "id": payload["id"], "result": {"tools": owner.tools}}
                    self.send_response(200)
                else:  # pragma: no cover - the assertion below makes this actionable
                    response = {"jsonrpc": "2.0", "id": payload["id"], "error": {"code": -32601, "message": "unexpected"}}
                    self.send_response(404)
                encoded = json.dumps(response).encode()
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(encoded)))
                self.end_headers()
                self.wfile.write(encoded)

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)

    @property
    def url(self) -> str:
        return f"http://127.0.0.1:{self.server.server_port}/mcp"

    def start(self) -> None:
        self.thread.start()

    def close(self) -> None:
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)
        assert not self.thread.is_alive()


@contextmanager
def _downstreams(audit_path: Path) -> Iterator[tuple[_Downstream, _Downstream]]:
    github = _Downstream(
        [
            {"name": "search", "inputSchema": {"type": "object"}},
            {"name": "hidden", "inputSchema": {"type": "object"}},
        ],
        {"content": [{"type": "text", "text": "matched"}], "structuredContent": {"count": 1}, "_meta": {"source": "mock"}},
        audit_path,
    )
    misc = _Downstream([{"name": "z", "inputSchema": {"type": "object"}}], {"content": []}, audit_path)
    github.start()
    misc.start()
    try:
        yield github, misc
    finally:
        github.close()
        misc.close()


def _headers(method: str, **extra: str) -> dict[str, str]:
    headers = {
        "Content-Type": "application/json",
        "Accept": "application/json, text/event-stream",
        "MCP-Protocol-Version": MCP_PROTOCOL_VERSION,
        "Mcp-Method": method,
    }
    headers.update(extra)
    return headers


def _body(method: str, request_id: int, params: dict[str, Any] | None = None) -> dict[str, Any]:
    return {
        "jsonrpc": "2.0",
        "id": request_id,
        "method": method,
        "params": {"_meta": {"io.modelcontextprotocol/protocolVersion": MCP_PROTOCOL_VERSION, "io.modelcontextprotocol/clientCapabilities": {}}, **(params or {})},
    }


def _wait_ready(client: TestClient) -> None:
    deadline = time.monotonic() + 3
    while time.monotonic() < deadline:
        if client.get("/healthz").json()["ready"]:
            return
        time.sleep(0.01)
    pytest.fail("gateway did not become ready after local downstream discovery")


def test_public_federation_policy_dispatch_audit_and_session_cleanup(tmp_path: Path) -> None:
    audit_path = tmp_path / "audit.jsonl"
    with _downstreams(audit_path) as (github, misc):
        config = parse_config(
            yaml.safe_load(
                f"""
listener: {{address: 127.0.0.1:8080, allowed_origins: []}}
audit: {{path: {audit_path}}}
policy: {{allow: [github.*, misc.*], deny: [github.hidden]}}
limits:
  max_pages_per_downstream: 4
  max_tools_per_downstream: 10
  max_downstream_catalog_bytes: 65536
  max_aggregate_tools: 10
  max_aggregate_response_bytes: 65536
  max_tool_response_bytes: 65536
  catalog_refresh_interval: 60s
  request_timeout: 1s
  tool_call_timeout: 1s
downstreams:
  - id: github
    url: {github.url}
    headers: {{X-Configured: configured-only}}
  - id: misc
    url: {misc.url}
    headers: {{}}
"""
            )
        )
        with TestClient(create_app(config, build_version="test")) as client:
            _wait_ready(client)
            discovered = client.post("/mcp", headers=_headers("server/discover"), json=_body("server/discover", 1))
            assert discovered.status_code == 200
            assert discovered.json()["id"] == 1

            listed = client.post("/mcp", headers=_headers("tools/list"), json=_body("tools/list", 2))
            assert [tool["name"] for tool in listed.json()["result"]["tools"]] == ["github.search", "misc.z"]

            called = client.post(
                "/mcp",
                headers=_headers("tools/call", **{"Mcp-Name": "github.search", "Authorization": "Bearer caller-secret", "Cookie": "caller=session", "X-Caller": "not-forwarded"}),
                json=_body("tools/call", 3, {"name": "github.search", "arguments": {"query": "dispatcher"}}),
            )
            assert called.status_code == 200
            assert called.json()["id"] == 3
            assert called.json()["result"]["structuredContent"] == {"count": 1}
            assert github.authorized_before_call
            calls = [request for request in github.requests if request[0] == "POST" and request[2]["method"] == "tools/call"]
            assert len(calls) == 1
            headers = {name.lower(): value for name, value in calls[0][1].items()}
            assert headers["x-configured"] == "configured-only"
            assert "authorization" not in headers and "cookie" not in headers and "x-caller" not in headers
            deletes = [request for request in github.requests if request[0] == "DELETE"]
            assert len(deletes) == 1
            assert {name.lower(): value for name, value in deletes[0][1].items()}["mcp-session-id"] == "owned-session"

            denied = client.post("/mcp", headers=_headers("tools/call", **{"Mcp-Name": "github.hidden"}), json=_body("tools/call", 4, {"name": "github.hidden", "arguments": {}}))
            assert denied.json()["error"]["code"] == -32010
            unknown = client.post("/mcp", headers=_headers("tools/call", **{"Mcp-Name": "github.unknown"}), json=_body("tools/call", 5, {"name": "github.unknown", "arguments": {}}))
            assert unknown.json()["error"]["code"] == -32010
            assert len([request for request in github.requests if request[0] == "POST" and request[2]["method"] == "tools/call"]) == 1
