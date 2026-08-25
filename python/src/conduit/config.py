"""Strict Phase 1 configuration loading compatible with the Go schema."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
import ipaddress
import re
from types import MappingProxyType
from typing import Any, Mapping
from urllib.parse import urlsplit

import yaml
from yaml.constructor import ConstructorError
from yaml.resolver import BaseResolver


DEFAULT_LISTENER_ADDRESS = "127.0.0.1:8080"
DEFAULT_REQUEST_TIMEOUT_SECONDS = 10.0


class ConfigError(ValueError):
    """Raised when a Conduit configuration is absent or invalid."""


class _StrictSafeLoader(yaml.SafeLoader):
    """Safe YAML loading that also rejects duplicate mapping keys."""


def _construct_unique_mapping(loader: _StrictSafeLoader, node: yaml.MappingNode, deep: bool = False) -> dict[object, object]:
    mapping: dict[object, object] = {}
    for key_node, value_node in node.value:
        key = loader.construct_object(key_node, deep=deep)
        if key in mapping:
            raise ConstructorError("while constructing a mapping", node.start_mark, f"duplicate key {key!r}", key_node.start_mark)
        mapping[key] = loader.construct_object(value_node, deep=deep)
    return mapping


_StrictSafeLoader.add_constructor(BaseResolver.DEFAULT_MAPPING_TAG, _construct_unique_mapping)


@dataclass(frozen=True)
class ListenerConfig:
    address: str
    allowed_origins: tuple[str, ...]


@dataclass(frozen=True)
class AuditConfig:
    path: str


@dataclass(frozen=True)
class PolicyConfig:
    allow: tuple[str, ...]
    deny: tuple[str, ...]


@dataclass(frozen=True)
class LimitsConfig:
    max_pages_per_downstream: int
    max_tools_per_downstream: int
    max_downstream_catalog_bytes: int
    max_aggregate_tools: int
    max_aggregate_response_bytes: int
    max_tool_response_bytes: int
    catalog_refresh_interval_seconds: float
    request_timeout_seconds: float
    tool_call_timeout_seconds: float


@dataclass(frozen=True)
class DownstreamConfig:
    id: str
    url: str
    headers: Mapping[str, str]


@dataclass(frozen=True)
class Config:
    listener: ListenerConfig
    audit: AuditConfig
    policy: PolicyConfig
    limits: LimitsConfig
    downstreams: tuple[DownstreamConfig, ...]


_TOP_LEVEL_KEYS = {"listener", "audit", "policy", "limits", "downstreams"}
_LISTENER_KEYS = {"address", "allowed_origins"}
_AUDIT_KEYS = {"path"}
_POLICY_KEYS = {"allow", "deny"}
_LIMIT_KEYS = {
    "max_pages_per_downstream",
    "max_tools_per_downstream",
    "max_downstream_catalog_bytes",
    "max_aggregate_tools",
    "max_aggregate_response_bytes",
    "max_tool_response_bytes",
    "catalog_refresh_interval",
    "request_timeout",
    "tool_call_timeout",
}
_DOWNSTREAM_KEYS = {"id", "url", "headers"}
_RULE_RE = re.compile(r"[^\s]+(?:\.\*)?$")
_DURATION_PART_RE = re.compile(r"(?P<value>[0-9]+(?:\.[0-9]+)?)(?P<unit>ns|us|µs|ms|s|m|h)")


def load_config(path: str | Path) -> Config:
    """Load one strict YAML document and validate the implemented Go schema."""

    config_path = Path(path)
    try:
        text = config_path.read_text(encoding="utf-8")
    except OSError as exc:
        raise ConfigError(f"read configuration: {exc}") from exc

    try:
        documents = list(yaml.load_all(text, Loader=_StrictSafeLoader))
    except yaml.YAMLError as exc:
        raise ConfigError(f"decode configuration: {exc}") from exc
    if len(documents) != 1:
        raise ConfigError("configuration must contain exactly one YAML document")
    return parse_config(documents[0])


def parse_config(value: object) -> Config:
    """Parse a decoded YAML value without coercing invalid scalar types."""

    root = _mapping(value, "configuration")
    _known_keys(root, _TOP_LEVEL_KEYS, "configuration")

    listener_raw = _optional_mapping(root, "listener")
    _known_keys(listener_raw, _LISTENER_KEYS, "listener")
    address = _optional_string(listener_raw, "address", DEFAULT_LISTENER_ADDRESS)
    allowed_origins = _string_sequence(listener_raw.get("allowed_origins", []), "listener.allowed_origins")
    _validate_listener(address)
    for origin in allowed_origins:
        _validate_origin(origin)

    audit_raw = _required_mapping(root, "audit")
    _known_keys(audit_raw, _AUDIT_KEYS, "audit")
    audit_path = _required_string(audit_raw, "path", "audit")
    if Path(audit_path) == Path("."):
        raise ConfigError("audit.path must name a file")

    policy_raw = _optional_mapping(root, "policy")
    _known_keys(policy_raw, _POLICY_KEYS, "policy")
    allow = _string_sequence(policy_raw.get("allow", []), "policy.allow")
    deny = _string_sequence(policy_raw.get("deny", []), "policy.deny")
    for rule in (*allow, *deny):
        _validate_rule(rule)

    limits_raw = _required_mapping(root, "limits")
    _known_keys(limits_raw, _LIMIT_KEYS, "limits")
    limits = LimitsConfig(
        max_pages_per_downstream=_positive_int(limits_raw, "max_pages_per_downstream"),
        max_tools_per_downstream=_positive_int(limits_raw, "max_tools_per_downstream"),
        max_downstream_catalog_bytes=_positive_int(limits_raw, "max_downstream_catalog_bytes"),
        max_aggregate_tools=_positive_int(limits_raw, "max_aggregate_tools"),
        max_aggregate_response_bytes=_positive_int(limits_raw, "max_aggregate_response_bytes"),
        max_tool_response_bytes=_positive_int(limits_raw, "max_tool_response_bytes"),
        catalog_refresh_interval_seconds=_positive_duration(limits_raw, "catalog_refresh_interval"),
        request_timeout_seconds=_request_timeout(limits_raw),
        tool_call_timeout_seconds=_positive_duration(limits_raw, "tool_call_timeout"),
    )

    downstreams_raw = root.get("downstreams")
    if not isinstance(downstreams_raw, list):
        raise ConfigError("downstreams must be a list")
    downstreams = tuple(_parse_downstream(item, index) for index, item in enumerate(downstreams_raw))
    if not downstreams:
        raise ConfigError("at least one downstream is required")
    _validate_unique_downstream_ids(downstreams)

    return Config(
        listener=ListenerConfig(address=address, allowed_origins=allowed_origins),
        audit=AuditConfig(path=audit_path),
        policy=PolicyConfig(allow=allow, deny=deny),
        limits=limits,
        downstreams=downstreams,
    )


def _parse_downstream(value: object, index: int) -> DownstreamConfig:
    label = f"downstreams[{index}]"
    raw = _mapping(value, label)
    _known_keys(raw, _DOWNSTREAM_KEYS, label)
    downstream_id = _required_string(raw, "id", label)
    url = _required_string(raw, "url", label)
    _validate_downstream_url(url)
    headers_raw = raw.get("headers", {})
    headers = _string_mapping(headers_raw, f"{label}.headers")
    if any(_protected_header(name) for name in headers):
        raise ConfigError(f"{label}.headers cannot set MCP routing or session headers")
    return DownstreamConfig(id=downstream_id, url=url, headers=headers)


def _mapping(value: object, label: str) -> Mapping[str, object]:
    if not isinstance(value, dict) or not all(isinstance(key, str) for key in value):
        raise ConfigError(f"{label} must be a mapping")
    return value


def _optional_mapping(root: Mapping[str, object], key: str) -> Mapping[str, object]:
    value = root.get(key, {})
    return _mapping(value, key)


def _required_mapping(root: Mapping[str, object], key: str) -> Mapping[str, object]:
    if key not in root:
        raise ConfigError(f"{key} is required")
    return _mapping(root[key], key)


def _known_keys(value: Mapping[str, object], allowed: set[str], label: str) -> None:
    unknown = sorted(set(value) - allowed)
    if unknown:
        raise ConfigError(f"{label} contains unknown field {unknown[0]!r}")


def _required_string(root: Mapping[str, object], key: str, label: str) -> str:
    if key not in root:
        raise ConfigError(f"{label}.{key} is required")
    value = root[key]
    if not isinstance(value, str) or not value:
        raise ConfigError(f"{label}.{key} must be a non-empty string")
    return value


def _optional_string(root: Mapping[str, object], key: str, default: str) -> str:
    if key not in root:
        return default
    value = root[key]
    if not isinstance(value, str):
        raise ConfigError(f"{key} must be a string")
    return value or default


def _string_sequence(value: object, label: str) -> tuple[str, ...]:
    if not isinstance(value, list) or not all(isinstance(item, str) for item in value):
        raise ConfigError(f"{label} must be a list of strings")
    return tuple(value)


def _string_mapping(value: object, label: str) -> Mapping[str, str]:
    if not isinstance(value, dict) or not all(isinstance(key, str) and isinstance(item, str) for key, item in value.items()):
        raise ConfigError(f"{label} must be a mapping of strings")
    return MappingProxyType(dict(value))


def _positive_int(root: Mapping[str, object], key: str) -> int:
    value = root.get(key)
    if isinstance(value, bool) or not isinstance(value, int) or value <= 0:
        raise ConfigError(f"limits.{key} must be a positive integer")
    return value


def _duration(value: object, label: str) -> float:
    if not isinstance(value, str):
        raise ConfigError(f"{label} must be a duration string")
    sign = -1.0 if value.startswith("-") else 1.0
    remaining = value[1:] if value.startswith(("-", "+")) else value
    if remaining == "0":
        return 0.0
    position = 0
    seconds = 0.0
    multipliers = {"ns": 1e-9, "us": 1e-6, "µs": 1e-6, "ms": 0.001, "s": 1.0, "m": 60.0, "h": 3600.0}
    while position < len(remaining):
        match = _DURATION_PART_RE.match(remaining, position)
        if match is None:
            raise ConfigError(f"{label} must use Go duration syntax")
        seconds += float(match.group("value")) * multipliers[match.group("unit")]
        position = match.end()
    return sign * seconds


def _positive_duration(root: Mapping[str, object], key: str) -> float:
    seconds = _duration(root.get(key), f"limits.{key}")
    if seconds <= 0:
        raise ConfigError(f"limits.{key} must be positive")
    return seconds


def _request_timeout(root: Mapping[str, object]) -> float:
    if "request_timeout" not in root:
        return DEFAULT_REQUEST_TIMEOUT_SECONDS
    seconds = _duration(root["request_timeout"], "limits.request_timeout")
    if seconds < 0:
        raise ConfigError("limits.request_timeout must not be negative")
    return seconds or DEFAULT_REQUEST_TIMEOUT_SECONDS


def _validate_listener(address: str) -> None:
    host, port = _split_host_port(address, "listener.address")
    if host not in {"127.0.0.1", "::1"}:
        raise ConfigError("listener.address must use a loopback host")
    if not port:
        raise ConfigError("listener.address must be host:port")


def _validate_origin(origin: str) -> None:
    parsed = urlsplit(origin)
    try:
        port = parsed.port
    except ValueError as exc:
        raise ConfigError("listener.allowed_origins entries must be http(s) origins with an explicit port") from exc
    if parsed.scheme not in {"http", "https"} or not parsed.hostname or port is None:
        raise ConfigError("listener.allowed_origins entries must be http(s) origins with an explicit port")
    if parsed.path or parsed.query or parsed.fragment or parsed.username or parsed.password:
        raise ConfigError("listener.allowed_origins entries must be origins only")
    if not _is_loopback_host(parsed.hostname):
        raise ConfigError("listener.allowed_origins entries must use loopback hosts")


def _split_host_port(address: str, label: str) -> tuple[str, str]:
    if address.startswith("["):
        end = address.find("]")
        if end == -1 or len(address) <= end + 2 or address[end + 1] != ":":
            raise ConfigError(f"{label} must be host:port")
        return address[1:end], address[end + 2 :]
    if address.count(":") != 1:
        raise ConfigError(f"{label} must be host:port")
    return tuple(address.split(":", 1))  # type: ignore[return-value]


def _is_loopback_host(host: str) -> bool:
    if host.lower() == "localhost":
        return True
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return False


def _validate_downstream_url(value: str) -> None:
    parsed = urlsplit(value)
    if parsed.scheme not in {"http", "https"} or not parsed.hostname or parsed.username or parsed.password:
        raise ConfigError("downstream url must be an http(s) URL without user info")


def _protected_header(value: str) -> bool:
    lowered = value.lower()
    return lowered in {"mcp-protocol-version", "mcp-method", "mcp-name", "mcp-session-id"} or lowered.startswith("mcp-param-")


def _validate_unique_downstream_ids(downstreams: tuple[DownstreamConfig, ...]) -> None:
    ids = [downstream.id for downstream in downstreams]
    if len(set(ids)) != len(ids):
        raise ConfigError("downstream ids must be unique")


def _validate_rule(rule: str) -> None:
    if not rule or not _RULE_RE.fullmatch(rule) or "*" in rule[:-2]:
        raise ConfigError("policy rules must be exact names or a trailing .*")
