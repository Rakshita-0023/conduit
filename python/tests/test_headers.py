from __future__ import annotations

import pytest

from conduit.headers import HeaderError, generate_headers, validate_call_headers


def _tool() -> dict[str, object]:
    return {"inputSchema": {"type": "object", "properties": {"count": {"type": "integer", "x-mcp-header": "Count"}, "token": {"type": "string", "x-mcp-header": "Token"}}}}


def test_numeric_header_parity_accepts_equivalent_decimal_integer() -> None:
    headers = generate_headers(_tool(), {"count": 42.0, "token": "alpha"})
    assert headers == {"Mcp-Param-Count": "42", "Mcp-Param-Token": "alpha"}
    validate_call_headers(_tool(), "server.tool", {"count": 42, "token": "alpha"}, {"mcp-name": "server.tool", "mcp-param-count": "42.0", "mcp-param-token": "alpha"})


def test_rejects_unsafe_integer() -> None:
    with pytest.raises(HeaderError):
        generate_headers(_tool(), {"count": 2**53, "token": "alpha"})
