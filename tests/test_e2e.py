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
            {
                "name": "with_header",
                "inputSchema": {
                    "type": "object",
                    "properties": {"token": {"type": "string", "x-mcp-header": "Token"}},
                },
            },
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


def _compat_headers(protocol_version: str, session_id: str | None = None, **extra: str) -> dict[str, str]:
    headers = {
        "Content-Type": "application/json",
        "Accept": "application/json, text/event-stream",
        "MCP-Protocol-Version": protocol_version,
    }
    if session_id is not None:
        headers["MCP-Session-Id"] = session_id
    headers.update(extra)
    return headers


def _compat_initialize(request_id: int | str, protocol_version: str) -> dict[str, Any]:
    return {
        "jsonrpc": "2.0",
        "id": request_id,
        "method": "initialize",
        "params": {
            "protocolVersion": protocol_version,
            "capabilities": {},
            "clientInfo": {"name": "standard-client", "version": "test"},
        },
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
            assert [tool["name"] for tool in listed.json()["result"]["tools"]] == ["github.search", "github.with_header", "misc.z"]

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

            valid_header = client.post(
                "/mcp",
                headers=_headers("tools/call", **{"Mcp-Name": "github.with_header", "Mcp-Param-Token": "alpha"}),
                json=_body("tools/call", int(1e9), {"name": "github.with_header", "arguments": {"token": "alpha"}}),
            )
            assert valid_header.status_code == 200

            missing_header = client.post(
                "/mcp",
                headers=_headers("tools/call", **{"Mcp-Name": "github.with_header"}),
                json=_body("tools/call", 9007199254740993, {"name": "github.with_header", "arguments": {"token": "alpha"}}),
            )
            assert missing_header.json()["error"]["code"] == -32010
            assert b'"id":9007199254740993,' in missing_header.content

            conflicting_header = client.post(
                "/mcp",
                headers=_headers("tools/call", **{"Mcp-Name": "github.with_header", "Mcp-Param-Token": "beta"}),
                json=_body("tools/call", 6, {"name": "github.with_header", "arguments": {"token": "alpha"}}),
            )
            assert conflicting_header.json()["error"]["code"] == -32010

            malformed_header = client.post(
                "/mcp",
                headers=_headers("tools/call", **{"Mcp-Name": "github.with_header", "Mcp-Param-Token": "=?base64?%%%?="}),
                json=_body("tools/call", 8, {"name": "github.with_header", "arguments": {"token": "alpha"}}),
            )
            assert malformed_header.json()["error"]["code"] == -32010

            unexpected_header = client.post(
                "/mcp",
                headers=_headers("tools/call", **{"Mcp-Name": "github.with_header", "Mcp-Param-Other": "alpha", "Mcp-Param-Token": "alpha"}),
                json=_body("tools/call", 7, {"name": "github.with_header", "arguments": {"token": "alpha"}}),
            )
            assert unexpected_header.json()["error"]["code"] == -32010

            denied = client.post("/mcp", headers=_headers("tools/call", **{"Mcp-Name": "github.hidden"}), json=_body("tools/call", 4, {"name": "github.hidden", "arguments": {}}))
            assert denied.json()["error"]["code"] == -32010
            unknown = client.post("/mcp", headers=_headers("tools/call", **{"Mcp-Name": "github.unknown"}), json=_body("tools/call", 5, {"name": "github.unknown", "arguments": {}}))
            assert unknown.json()["error"]["code"] == -32010
            assert len([request for request in github.requests if request[0] == "POST" and request[2]["method"] == "tools/call"]) == 2


@pytest.mark.parametrize("protocol_version", ["2025-06-18", "2025-11-25"])
def test_standard_streamable_http_is_normalized_into_the_existing_gateway_path(tmp_path: Path, protocol_version: str) -> None:
    """Exercise the public client adapter, not internal policy or dispatch APIs."""

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
            initialized = client.post("/mcp", headers=_compat_headers(protocol_version), json=_compat_initialize("compat-init", protocol_version))
            assert initialized.status_code == 200
            assert initialized.json() == {
                "jsonrpc": "2.0",
                "id": "compat-init",
                "result": {
                    "protocolVersion": protocol_version,
                    "capabilities": {"tools": {"listChanged": False}},
                    "serverInfo": {"name": "conduit", "version": "test"},
                },
            }
            session_id = initialized.headers["mcp-session-id"]
            assert client.post("/mcp", headers=_compat_headers(protocol_version, session_id), json={"jsonrpc": "2.0", "method": "notifications/initialized"}).status_code == 202
            event_channel = client.get("/mcp", headers={"MCP-Protocol-Version": protocol_version, "MCP-Session-Id": session_id})
            assert event_channel.status_code == 200
            assert event_channel.headers["content-type"].startswith("text/event-stream")
            assert event_channel.content == b": conduit does not emit server events\n\n"

            listed = client.post("/mcp", headers=_compat_headers(protocol_version, session_id), json={"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
            assert listed.status_code == 200
            assert [tool["name"] for tool in listed.json()["result"]["tools"]] == ["github.search", "github.with_header", "misc.z"]
            assert listed.headers["mcp-session-id"] == session_id

            called = client.post(
                "/mcp",
                headers=_compat_headers(protocol_version, session_id),
                json={"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "github.search", "arguments": {"query": "compat"}}},
            )
            assert called.status_code == 200
            assert called.json()["result"]["structuredContent"] == {"count": 1}
            assert github.authorized_before_call

            # Compatibility only supplies the native correlation header.  It
            # must leave x-mcp-header validation, policy, and audit ordering in
            # the single existing dispatch path.
            missing_parameter_header = client.post(
                "/mcp",
                headers=_compat_headers(protocol_version, session_id),
                json={
                    "jsonrpc": "2.0",
                    "id": 9007199254740993,
                    "method": "tools/call",
                    "params": {"name": "github.with_header", "arguments": {"token": "alpha"}},
                },
            )
            assert missing_parameter_header.json()["error"]["code"] == -32010
            assert b'"id":9007199254740993,' in missing_parameter_header.content

            # Client-supplied routing metadata is checked, but a standard MCP
            # client need not know Conduit's native Mcp-Name correlation header.
            mismatched_name = client.post(
                "/mcp",
                headers=_compat_headers(protocol_version, session_id, **{"Mcp-Name": "misc.z"}),
                json={"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "github.search", "arguments": {}}},
            )
            assert mismatched_name.status_code == 400
            denied = client.post(
                "/mcp",
                headers=_compat_headers(protocol_version, session_id),
                json={"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": {"name": "github.hidden", "arguments": {}}},
            )
            assert denied.json()["error"]["code"] == -32010
            assert len([item for item in github.requests if item[0] == "POST" and item[2]["method"] == "tools/call"]) == 1

            assert client.delete("/mcp", headers={"MCP-Protocol-Version": protocol_version, "MCP-Session-Id": session_id}).status_code == 204
            stale = client.post("/mcp", headers=_compat_headers(protocol_version, session_id), json={"jsonrpc": "2.0", "id": 5, "method": "tools/list"})
            assert stale.status_code == 400
            assert stale.text == "invalid MCP session\n"


def test_standard_sessions_are_version_bound_and_independent(tmp_path: Path) -> None:
    audit_path = tmp_path / "audit.jsonl"
    with _downstreams(audit_path) as (github, misc):
        config = parse_config(
            yaml.safe_load(
                f"""
listener: {{address: 127.0.0.1:8080, allowed_origins: []}}
audit: {{path: {audit_path}}}
policy: {{allow: [github.*, misc.*], deny: []}}
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
    headers: {{}}
  - id: misc
    url: {misc.url}
    headers: {{}}
"""
            )
        )
        with TestClient(create_app(config, build_version="test")) as client:
            _wait_ready(client)
            first = client.post("/mcp", headers=_compat_headers("2025-06-18"), json=_compat_initialize(1, "2025-06-18"))
            second = client.post("/mcp", headers=_compat_headers("2025-11-25"), json=_compat_initialize(2, "2025-11-25"))
            first_session = first.headers["mcp-session-id"]
            second_session = second.headers["mcp-session-id"]
            assert first_session != second_session
            assert client.app.state.conduit_runtime.compatibility_sessions.count == 2

            # A token has one protocol binding and cannot be repurposed by a
            # different negotiated client/version.
            crossed = client.post(
                "/mcp",
                headers=_compat_headers("2025-11-25", first_session),
                json={"jsonrpc": "2.0", "id": 3, "method": "tools/list"},
            )
            assert crossed.status_code == 400
            assert client.post(
                "/mcp",
                headers=_compat_headers("2025-06-18", first_session),
                json={"jsonrpc": "2.0", "id": 4, "method": "tools/list"},
            ).status_code == 200
            assert client.post(
                "/mcp",
                headers=_compat_headers("2025-11-25", second_session),
                json={"jsonrpc": "2.0", "id": 5, "method": "tools/list"},
            ).status_code == 200
            assert client.delete("/mcp", headers={"MCP-Protocol-Version": "2025-06-18", "MCP-Session-Id": first_session}).status_code == 204
            assert client.delete("/mcp", headers={"MCP-Protocol-Version": "2025-11-25", "MCP-Session-Id": second_session}).status_code == 204
            assert client.app.state.conduit_runtime.compatibility_sessions.count == 0
