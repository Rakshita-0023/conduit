from __future__ import annotations

import pytest

from conduit.protocol import MCP_PROTOCOL_VERSION, ProtocolError, json_rpc_error, validate_mcp_request


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
