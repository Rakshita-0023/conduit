"""The deliberately narrow MCP 2026-07-28 JSON-RPC wire profile."""

from __future__ import annotations

import json
import re
from dataclasses import dataclass
from typing import Any, Mapping

MCP_PROTOCOL_VERSION = "2026-07-28"
# These are the session-based Streamable HTTP versions observed in currently
# supported MCP clients.  They are deliberately kept separate from Conduit's
# native 2026 profile: compatibility requests are normalized at ingress and
# never change the protocol used between Conduit and a downstream.
COMPAT_PROTOCOL_VERSIONS = frozenset({"2025-06-18", "2025-11-25"})
MAX_INGRESS_BODY_BYTES = 1 << 20
_NUMBER_RE = re.compile(r"-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$")


class ProtocolError(ValueError):
    """A request did not meet Conduit's supported public MCP profile."""


@dataclass(frozen=True)
class MCPRequest:
    method: str
    params: Mapping[str, Any]
    raw_id: str


@dataclass(frozen=True)
class CompatibilityRequest:
    """A standard session-based MCP request accepted by the ingress adapter."""

    method: str
    params: Mapping[str, Any]
    raw_id: str | None


def validate_mcp_request(body: bytes, headers: Mapping[str, str]) -> MCPRequest:
    """Validate an MCP request and preserve the raw JSON-RPC ID token."""

    if len(body) > MAX_INGRESS_BODY_BYTES:
        raise ProtocolError("request body is too large")
    try:
        text = body.decode("utf-8")
        value = _strict_loads(text)
        raw_id = _raw_top_level_id(text)
    except (UnicodeDecodeError, ValueError, json.JSONDecodeError) as exc:
        raise ProtocolError("invalid JSON") from exc

    if not isinstance(value, dict):
        raise ProtocolError("JSON-RPC request must be an object")
    if value.get("jsonrpc") != "2.0":
        raise ProtocolError("jsonrpc must be 2.0")
    method = value.get("method")
    if not isinstance(method, str) or not method:
        raise ProtocolError("method must be a non-empty string")
    if raw_id is None or not _valid_id(raw_id):
        raise ProtocolError("request ID must be a JSON string or number")
    params = value.get("params", {})
    if not isinstance(params, dict):
        raise ProtocolError("params must be an object")
    _validate_meta(params)

    if headers.get("mcp-protocol-version") != MCP_PROTOCOL_VERSION:
        raise ProtocolError("unsupported MCP protocol version")
    if headers.get("mcp-method") != method:
        raise ProtocolError("Mcp-Method must match JSON-RPC method")
    return MCPRequest(method=method, params=params, raw_id=raw_id)


def validate_compatibility_request(body: bytes) -> CompatibilityRequest:
    """Validate common JSON-RPC framing without weakening the native profile.

    Standard Streamable HTTP clients negotiate their protocol in ``initialize``
    and a session header, rather than in Conduit's native metadata/header pair.
    This only validates framing; ingress applies method and session rules.
    """

    if len(body) > MAX_INGRESS_BODY_BYTES:
        raise ProtocolError("request body is too large")
    try:
        text = body.decode("utf-8")
        value = _strict_loads(text)
        raw_id = _raw_top_level_id(text)
    except (UnicodeDecodeError, ValueError, json.JSONDecodeError) as exc:
        raise ProtocolError("invalid JSON") from exc
    if not isinstance(value, dict):
        raise ProtocolError("JSON-RPC request must be an object")
    if value.get("jsonrpc") != "2.0":
        raise ProtocolError("jsonrpc must be 2.0")
    method = value.get("method")
    if not isinstance(method, str) or not method:
        raise ProtocolError("method must be a non-empty string")
    if raw_id is not None and not _valid_id(raw_id):
        raise ProtocolError("request ID must be a JSON string or number")
    params = value.get("params", {})
    if not isinstance(params, dict):
        raise ProtocolError("params must be an object")
    return CompatibilityRequest(method=method, params=params, raw_id=raw_id)


def compatibility_initialize_result(build_version: str, protocol_version: str) -> dict[str, Any]:
    """Return the standard MCP initialize result for a compatible client."""

    return {
        "protocolVersion": protocol_version,
        "capabilities": {"tools": {"listChanged": False}},
        "serverInfo": {"name": "conduit", "version": build_version},
    }


def json_rpc_error(raw_id: str, code: int, message: str, data: object | None = None) -> bytes:
    """Encode an error while retaining the client-provided JSON ID token."""

    prefix = b'{"jsonrpc":"2.0","id":'
    error_value: dict[str, object] = {"code": code, "message": message}
    if data is not None:
        error_value["data"] = data
    error = json.dumps(error_value, separators=(",", ":")).encode("utf-8")
    return prefix + raw_id.encode("utf-8") + b',"error":' + error + b"}\n"


def json_rpc_result(raw_id: str, result: Mapping[str, Any]) -> bytes:
    prefix = b'{"jsonrpc":"2.0","id":'
    encoded = json.dumps(_json_value(result), separators=(",", ":"), ensure_ascii=False).encode("utf-8")
    return prefix + raw_id.encode("utf-8") + b',"result":' + encoded + b"}\n"


def discovery_result(build_version: str) -> dict[str, Any]:
    """Return the stable discovery contract advertised by Go v0.1.x."""

    return {
        "resultType": "complete",
        "_meta": {
            "io.modelcontextprotocol/serverInfo": {
                "name": "conduit",
                "version": build_version,
            }
        },
        "ttlMs": 0,
        "cacheScope": "public",
        "supportedVersions": [MCP_PROTOCOL_VERSION],
        "capabilities": {"tools": {}},
    }


def _strict_loads(text: str) -> object:
    return json.loads(text, parse_constant=_reject_json_constant)


def _reject_json_constant(value: str) -> None:
    raise ValueError(f"invalid JSON constant {value}")


def _raw_top_level_id(text: str) -> str | None:
    """Find the final top-level id value, matching JSON object last-key semantics."""

    decoder = json.JSONDecoder(parse_constant=_reject_json_constant)
    index = _skip_space(text, 0)
    if index >= len(text) or text[index] != "{":
        return None
    index += 1
    result: str | None = None
    while True:
        index = _skip_space(text, index)
        if index >= len(text):
            raise ValueError("unterminated object")
        if text[index] == "}":
            return result
        key, index = decoder.raw_decode(text, index)
        if not isinstance(key, str):
            raise ValueError("object key must be a string")
        index = _skip_space(text, index)
        if index >= len(text) or text[index] != ":":
            raise ValueError("missing object colon")
        index = _skip_space(text, index + 1)
        start = index
        _, index = decoder.raw_decode(text, index)
        if key == "id":
            result = text[start:index]
        index = _skip_space(text, index)
        if index >= len(text):
            raise ValueError("unterminated object")
        if text[index] == "}":
            return result
        if text[index] != ",":
            raise ValueError("missing object comma")
        index += 1


def _skip_space(text: str, index: int) -> int:
    while index < len(text) and text[index] in " \t\r\n":
        index += 1
    return index


def _valid_id(raw_id: str) -> bool:
    if raw_id.startswith('"'):
        try:
            return isinstance(_strict_loads(raw_id), str)
        except (ValueError, json.JSONDecodeError):
            return False
    return bool(_NUMBER_RE.fullmatch(raw_id))


def _validate_meta(params: Mapping[str, Any]) -> None:
    meta = params.get("_meta")
    if not isinstance(meta, dict):
        raise ProtocolError("_meta is required")
    if meta.get("io.modelcontextprotocol/protocolVersion") != MCP_PROTOCOL_VERSION:
        raise ProtocolError("invalid protocol version metadata")
    if "io.modelcontextprotocol/clientCapabilities" not in meta:
        raise ProtocolError("client capabilities metadata is required")


def _json_value(value: Any) -> Any:
    """Turn immutable snapshot containers back into ordinary JSON values."""

    if isinstance(value, Mapping):
        return {key: _json_value(item) for key, item in value.items()}
    if isinstance(value, tuple):
        return [_json_value(item) for item in value]
    return value
