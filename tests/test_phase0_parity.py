"""Small Go-contract vectors meaningful before Phase 2 federation exists."""

from __future__ import annotations

import pytest
from starlette.testclient import TestClient

from conduit.app import create_app
from conduit.protocol import MCP_PROTOCOL_VERSION


def _request(method: str, request_id: str = "1") -> tuple[dict[str, str], bytes]:
    return (
        {
            "Content-Type": "application/json",
            "Accept": "application/json, text/event-stream",
            "MCP-Protocol-Version": MCP_PROTOCOL_VERSION,
            "Mcp-Method": method,
        },
        (
            '{"jsonrpc":"2.0","id":'
            + request_id
            + ',"method":"'
            + method
            + '","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}'
        ).encode(),
    )


@pytest.fixture
def client(config):
    with TestClient(create_app(config)) as test_client:
        yield test_client


@pytest.mark.parametrize(
    ("name", "headers", "body", "expected_status"),
    [
        ("not_ready_discover", *_request("server/discover"), 503),
        ("protocol_rejection", {**_request("server/discover")[0], "MCP-Protocol-Version": "2025-03-26"}, _request("server/discover")[1], 400),
        ("malformed_ingress", _request("server/discover")[0], b"{", 400),
        ("unsupported_method", *_request("prompts/list", "9007199254740993"), 404),
    ],
)
def test_phase0_go_ingress_contract_vectors(client: TestClient, name: str, headers, body, expected_status: int) -> None:
    response = client.post("/mcp", headers=headers, content=body)

    assert response.status_code == expected_status, name
    if name == "unsupported_method":
        assert b'"id":9007199254740993,' in response.content


def test_phase0_origin_rejection_vector(config) -> None:
    headers, body = _request("server/discover")
    headers["Origin"] = "https://example.invalid:443"
    with TestClient(create_app(config)) as client:
        response = client.post("/mcp", headers=headers, content=body)

    assert response.status_code == 403
