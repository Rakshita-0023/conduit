from __future__ import annotations

import pytest

from conduit.protocol import (
    COMPAT_PROTOCOL_VERSIONS,
    MCP_PROTOCOL_VERSION,
    ProtocolError,
    compatibility_initialize_result,
    json_rpc_error,
    validate_compatibility_request,
    validate_mcp_request,
)


def _headers(method: str) -> dict[str, str]:
    return {"mcp-protocol-version": MCP_PROTOCOL_VERSION, "mcp-method": method}


def test_protocol_preserves_escaped_and_exponent_ids_and_rejects_invalid_envelopes() -> None:
    body = b'{"jsonrpc":"2.0","id":"escaped\\u002did","method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}'
    request = validate_mcp_request(body, _headers("server/discover"))
    assert request.raw_id == '"escaped\\u002did"'
    assert b'"id":"escaped\\u002did"' in json_rpc_error(request.raw_id, -1, "bad")
    numeric = body.replace(b'"escaped\\u002did"', b'1e+9')
    assert validate_mcp_request(numeric, _headers("server/discover")).raw_id == "1e+9"
    for invalid in (b"{", body.replace(b'"2.0"', b'"1.0"', 1), body.replace(b'"escaped\\u002did"', b'null')):
        with pytest.raises(ProtocolError):
            validate_mcp_request(invalid, _headers("server/discover"))


def test_standard_compatibility_framing_preserves_raw_ids_without_accepting_invalid_ids() -> None:
    request = validate_compatibility_request(
        b'{"jsonrpc":"2.0","id":"compat\\u002did","method":"initialize","params":{"protocolVersion":"2025-11-25"}}'
    )
    assert request.method == "initialize"
    assert request.raw_id == '"compat\\u002did"'
    assert request.params == {"protocolVersion": "2025-11-25"}
    notification = validate_compatibility_request(b'{"jsonrpc":"2.0","method":"notifications/initialized"}')
    assert notification.raw_id is None
    for invalid in (b'{"jsonrpc":"2.0","id":null,"method":"tools/list"}', b'{"jsonrpc":"2.0","id":true,"method":"tools/list"}', b'{"jsonrpc":"2.0","id":1,"method":"tools/list","params":[]}'):
        with pytest.raises(ProtocolError):
            validate_compatibility_request(invalid)

    assert COMPAT_PROTOCOL_VERSIONS == {"2025-06-18", "2025-11-25"}
    assert compatibility_initialize_result("build", "2025-11-25") == {
        "protocolVersion": "2025-11-25",
        "capabilities": {"tools": {"listChanged": False}},
        "serverInfo": {"name": "conduit", "version": "build"},
    }
