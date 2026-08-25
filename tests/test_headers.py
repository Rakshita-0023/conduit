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


def test_header_validation_rejects_missing_unexpected_and_unsafe_text() -> None:
    annotated = _tool()
    encoded = generate_headers(annotated, {"count": 0, "token": " leading"})
    assert encoded["Mcp-Param-Token"].startswith("=?base64?")
    with pytest.raises(HeaderError):
        validate_call_headers(annotated, "server.tool", {"count": 1}, {"mcp-name": "server.tool"})
    with pytest.raises(HeaderError):
        validate_call_headers(annotated, "server.tool", {}, {"mcp-name": "server.tool", "mcp-param-count": "1"})
    with pytest.raises(HeaderError):
        generate_headers({"inputSchema": {"properties": {"bad": {"x-mcp-header": "Bad", "type": "array"}}}}, {"bad": []})


def test_header_schema_edge_cases_and_boolean_parity() -> None:
    assert generate_headers({}, {"x": 1}) == {}
    nested = {
        "inputSchema": {
            "properties": {
                "enabled": {"type": "boolean", "x-mcp-header": "Enabled"},
                "nested": {"properties": {"name": {"type": "string", "x-mcp-header": "Name"}}},
            }
        }
    }
    assert generate_headers(nested, {"enabled": False, "nested": {"name": "ok"}}) == {"Mcp-Param-Enabled": "false", "Mcp-Param-Name": "ok"}
    with pytest.raises(HeaderError):
        generate_headers({"inputSchema": {"properties": {"a": {"type": "string", "x-mcp-header": "same"}, "b": {"type": "string", "x-mcp-header": "Same"}}}}, {})
    with pytest.raises(HeaderError):
        validate_call_headers(_tool(), "server.tool", {"token": "x"}, {"mcp-name": "wrong"})
    with pytest.raises(HeaderError):
        generate_headers({"inputSchema": {"properties": {"bad": {"type": "string", "x-mcp-header": "not a header"}}}}, {"bad": "x"})
