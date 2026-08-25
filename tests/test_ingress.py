from __future__ import annotations

import pytest
from starlette.testclient import TestClient

from conduit.app import create_app
from conduit.protocol import MCP_PROTOCOL_VERSION, discovery_result


def _headers(method: str, **overrides: str) -> dict[str, str]:
    headers = {
        "Content-Type": "application/json",
        "Accept": "application/json, text/event-stream",
        "MCP-Protocol-Version": MCP_PROTOCOL_VERSION,
        "Mcp-Method": method,
    }
    headers.update(overrides)
    return headers


def _body(method: str, request_id: str = "42") -> bytes:
    return (
        '{"jsonrpc":"2.0","id":'
        + request_id
        + ',"method":"'
        + method
        + '","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}'
    ).encode()


@pytest.fixture
def client(config):
    app = create_app(config, build_version="test-build")
    with TestClient(app) as test_client:
        yield test_client


def test_healthz_and_status_preserve_unready_schema(client: TestClient) -> None:
    health = client.get("/healthz")
    status = client.get("/status")

    assert health.status_code == 200
    assert status.status_code == 200
    # The first refresh is deliberately concurrent with request handling.  It
    # may still be starting or may already have failed, but it must never
    # fabricate readiness without a usable catalog.
    for observed in (health.json(), status.json()):
        assert observed["live"] is True
        assert observed["ready"] is False
        assert observed["audit_healthy"] is True
        assert set(observed) == {"live", "ready", "audit_healthy", "aggregate", "downstreams"}
        assert observed["downstreams"][0]["id"] == "example"


def test_lifespan_marks_process_not_live_after_shutdown(config) -> None:
    app = create_app(config)
    with TestClient(app) as client:
        assert client.get("/healthz").json()["live"] is True
    assert app.state.conduit_health.snapshot()["live"] is False


def test_status_sorts_downstreams_by_id(config) -> None:
    import yaml

    from conduit.config import parse_config

    value = yaml.safe_load(
        """\
downstreams:
  - id: zeta
    url: http://127.0.0.1:9001/mcp
  - id: alpha
    url: http://127.0.0.1:9002/mcp
"""
    )
    value.update(
        {
            "listener": {"address": config.listener.address},
            "audit": {"path": config.audit.path},
            "policy": {},
            "limits": {
                "max_pages_per_downstream": 1,
                "max_tools_per_downstream": 1,
                "max_downstream_catalog_bytes": 1,
                "max_aggregate_tools": 1,
                "max_aggregate_response_bytes": 1,
                "max_tool_response_bytes": 1,
                "catalog_refresh_interval": "1s",
                "tool_call_timeout": "1s",
            },
        }
    )
    with TestClient(create_app(parse_config(value))) as client:
        status = client.get("/status").json()

    assert [downstream["id"] for downstream in status["downstreams"]] == ["alpha", "zeta"]


def test_valid_discover_is_truthfully_unavailable_before_catalog_startup(client: TestClient) -> None:
    response = client.post("/mcp", content=_body("server/discover"), headers=_headers("server/discover"))

    assert response.status_code == 503
    assert response.text == "conduit not ready\n"


def test_discovery_contract_matches_go_when_a_future_phase_marks_ready() -> None:
    assert discovery_result("test-build") == {
        "resultType": "complete",
        "_meta": {"io.modelcontextprotocol/serverInfo": {"name": "conduit", "version": "test-build"}},
        "ttlMs": 0,
        "cacheScope": "public",
        "supportedVersions": [MCP_PROTOCOL_VERSION],
        "capabilities": {"tools": {}},
    }


@pytest.mark.parametrize(
    ("headers", "body"),
    [
        (_headers("server/discover", **{"MCP-Protocol-Version": "2025-03-26"}), _body("server/discover")),
        (_headers("server/discover", **{"MCP-Protocol-Version": ""}), _body("server/discover")),
        (_headers("tools/list"), _body("server/discover")),
        (_headers("server/discover"), b"{"),
        (_headers("server/discover"), _body("server/discover").replace(b'"jsonrpc":"2.0"', b'"jsonrpc":"1.0"')),
        (_headers("server/discover"), _body("server/discover").replace(b"clientCapabilities\":{}", b"clientCapabilitiesMissing\":{}")),
        (_headers("server/discover"), _body("server/discover", "null")),
        (_headers("server/discover"), _body("server/discover", "{}")),
    ],
)
def test_rejects_invalid_mcp_requests(client: TestClient, headers: dict[str, str], body: bytes) -> None:
    response = client.post("/mcp", content=body, headers=headers)

    assert response.status_code == 400
    assert response.text == "invalid MCP request\n"


def test_unsupported_valid_method_returns_json_rpc_method_not_found(client: TestClient) -> None:
    response = client.post("/mcp", content=_body("prompts/list", '"exact-id"'), headers=_headers("prompts/list"))

    assert response.status_code == 404
    assert response.json() == {
        "jsonrpc": "2.0",
        "id": "exact-id",
        "error": {"code": -32601, "message": "method not found"},
    }


def test_transport_validation_precedes_unknown_method_error(client: TestClient) -> None:
    response = client.post(
        "/mcp",
        content=_body("prompts/list"),
        headers=_headers("prompts/list", Accept="application/json"),
    )

    assert response.status_code == 400
    assert response.text == "invalid Accept header\n"


def test_bad_content_type_is_rejected_after_mcp_validation(client: TestClient) -> None:
    response = client.post(
        "/mcp",
        content=_body("tools/list"),
        headers=_headers("tools/list", **{"Content-Type": "text/plain"}),
    )

    assert response.status_code == 415
    assert response.text == "unsupported media type\n"


def test_origin_allowlist_and_absent_origin(config) -> None:
    from dataclasses import replace

    from conduit.config import ListenerConfig

    allowed = replace(config, listener=ListenerConfig(config.listener.address, ("http://localhost:3000",)))
    with TestClient(create_app(allowed)) as client:
        assert client.post("/mcp", content=_body("server/discover"), headers=_headers("server/discover")).status_code == 503
        assert client.post(
            "/mcp",
            content=_body("server/discover"),
            headers=_headers("server/discover", Origin="http://localhost:3000"),
        ).status_code == 503
        assert client.post(
            "/mcp",
            content=_body("server/discover"),
            headers=_headers("server/discover", Origin="https://example.invalid:443"),
        ).status_code == 403
        assert client.post(
            "/mcp",
            content=_body("server/discover"),
            headers=_headers("server/discover", Origin="null"),
        ).status_code == 403


@pytest.mark.parametrize("request_id", ["42", '"string-id"', "9007199254740993", "1e+9"])
def test_unsupported_method_preserves_raw_request_id(client: TestClient, request_id: str) -> None:
    response = client.post("/mcp", content=_body("prompts/list", request_id), headers=_headers("prompts/list"))

    assert response.status_code == 404
    expected_fragment = b'"id":' + request_id.encode() + b","
    assert expected_fragment in response.content
    assert response.json()["error"]["code"] == -32601


def test_body_bound_is_rejected(client: TestClient) -> None:
    body = b" " * ((1 << 20) + 1)
    response = client.post("/mcp", content=body, headers=_headers("server/discover"))

    assert response.status_code == 400


def test_body_at_ingress_bound_is_admitted(client: TestClient) -> None:
    body = _body("server/discover")
    body += b" " * ((1 << 20) - len(body))

    response = client.post("/mcp", content=body, headers=_headers("server/discover"))

    assert response.status_code == 503


def test_not_found_and_method_restriction_match_http_boundary(client: TestClient) -> None:
    assert client.get("/absent").status_code == 404
    response = client.get("/mcp")
    assert response.status_code == 405
    assert response.headers["allow"] == "POST"
