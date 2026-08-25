"""MCP tool x-mcp-header validation and invocation header generation."""

from __future__ import annotations

import base64
from collections.abc import Mapping
from decimal import Decimal, InvalidOperation
from typing import Any


class HeaderError(ValueError):
    """An annotated tool header cannot be safely generated."""


def validate_call_headers(tool: Mapping[str, Any], public_name: str, arguments: Mapping[str, Any], headers: Mapping[str, str]) -> None:
    if headers.get("mcp-name") != public_name:
        raise HeaderError("Mcp-Name must match tool name")
    generated = generate_headers(tool, arguments)
    lowered = {key.lower(): value for key, value in headers.items()}
    for key, expected in generated.items():
        actual = lowered.get(key.lower())
        if actual is None or not _equivalent(expected, _decode(actual)):
            raise HeaderError("Mcp-Param header does not match arguments")
    expected_names = {key.lower() for key in generated}
    if any(name.startswith("mcp-param-") and name not in expected_names for name in lowered):
        raise HeaderError("unexpected Mcp-Param header")


def generate_headers(tool: Mapping[str, Any], arguments: Mapping[str, Any]) -> dict[str, str]:
    schema = tool.get("inputSchema")
    if not isinstance(schema, Mapping):
        return {}
    output: dict[str, str] = {}
    seen: set[str] = set()
    _collect(schema.get("properties"), arguments, output, seen)
    return output


def _collect(properties: object, arguments: Mapping[str, Any], output: dict[str, str], seen: set[str]) -> None:
    if not isinstance(properties, Mapping):
        return
    for name, property_schema in properties.items():
        if not isinstance(name, str) or not isinstance(property_schema, Mapping):
            continue
        annotation = property_schema.get("x-mcp-header")
        if annotation is not None:
            if not isinstance(annotation, str) or not annotation or not _header_token(annotation):
                raise HeaderError("invalid x-mcp-header annotation")
            lowered = annotation.lower()
            if lowered in seen:
                raise HeaderError("duplicate x-mcp-header annotation")
            seen.add(lowered)
            if name in arguments:
                value = arguments[name]
                expected_type = property_schema.get("type")
                output["Mcp-Param-" + annotation] = _encode(expected_type, value)
        nested = arguments.get(name)
        _collect(property_schema.get("properties"), nested if isinstance(nested, Mapping) else {}, output, seen)


def _encode(expected_type: object, value: object) -> str:
    if expected_type == "string" and isinstance(value, str):
        return _wire_encode(value)
    if expected_type == "boolean" and isinstance(value, bool):
        return "true" if value else "false"
    if expected_type == "integer" and isinstance(value, (int, float)) and not isinstance(value, bool):
        number = Decimal(str(value))
        if number == number.to_integral_value() and -(2**53 - 1) <= number <= 2**53 - 1:
            return str(int(number))
    if expected_type not in {"string", "boolean", "integer"}:
        raise HeaderError("invalid x-mcp-header annotation type")
    raise HeaderError("invalid x-mcp-header argument")


def _header_token(value: str) -> bool:
    return all(char.isalnum() or char in "!#$%&'*+-.^_`|~" for char in value)


def _wire_encode(value: str) -> str:
    if not value:
        return value
    unsafe = value[0].isspace() or value[-1].isspace() or any(ord(char) < 0x20 or ord(char) > 0x7E for char in value)
    if value.startswith("=?base64?") and value.endswith("?="):
        unsafe = True
    return f"=?base64?{base64.b64encode(value.encode()).decode()}?=" if unsafe else value


def _decode(value: str) -> str:
    if value.startswith("=?base64?") and value.endswith("?="):
        try:
            return base64.b64decode(value[9:-2], validate=True).decode()
        except (ValueError, UnicodeDecodeError):
            return "\0invalid"
    return value


def _equivalent(expected: str, actual: str) -> bool:
    try:
        left, right = Decimal(expected), Decimal(actual)
    except InvalidOperation:
        return expected == actual
    if left != left.to_integral_value() or right != right.to_integral_value():
        return False
    maximum = Decimal(2**53 - 1)
    return -maximum <= left <= maximum and -maximum <= right <= maximum and left == right
