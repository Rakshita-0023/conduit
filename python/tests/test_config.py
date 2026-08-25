from __future__ import annotations

import textwrap

import pytest
import yaml

from conduit.config import (
    DEFAULT_LISTENER_ADDRESS,
    DEFAULT_REQUEST_TIMEOUT_SECONDS,
    ConfigError,
    load_config,
    parse_config,
)


def test_loads_valid_config_and_preserves_defaults(tmp_path, config_text: str) -> None:
    path = tmp_path / "conduit.yaml"
    path.write_text(config_text)

    config = load_config(path)

    assert config.listener.address == "127.0.0.1:8080"
    assert config.limits.request_timeout_seconds == 10.0
    assert config.downstreams[0].headers == {"X-Downstream-Key": "test-only"}


def test_listener_address_and_request_timeout_default(config_text: str) -> None:
    value = yaml.safe_load(config_text)
    del value["listener"]["address"]
    del value["limits"]["request_timeout"]

    config = parse_config(value)

    assert config.listener.address == DEFAULT_LISTENER_ADDRESS
    assert config.limits.request_timeout_seconds == DEFAULT_REQUEST_TIMEOUT_SECONDS


@pytest.mark.parametrize(
    ("mutate", "message"),
    [
        (lambda value: value.__setitem__("unexpected", True), "unknown field"),
        (lambda value: value["limits"].__setitem__("max_tools_per_downstream", True), "positive integer"),
        (lambda value: value["listener"].__setitem__("address", "0.0.0.0:8080"), "loopback"),
        (lambda value: value["listener"].__setitem__("address", "localhost:8080"), "loopback"),
        (lambda value: value["listener"].__setitem__("allowed_origins", ["https://example.com:8443"]), "loopback"),
        (lambda value: value["listener"].__setitem__("allowed_origins", "http://127.0.0.1:8080"), "list of strings"),
        (lambda value: value["downstreams"][0].__setitem__("url", "https://user:pass@127.0.0.1:9000/mcp"), "without user info"),
        (lambda value: value.__setitem__("downstreams", []), "at least one downstream"),
        (lambda value: value["policy"].__setitem__("allow", ["bad*rule"]), "policy rules"),
    ],
)
def test_rejects_invalid_configuration(config_text: str, mutate, message: str) -> None:
    value = yaml.safe_load(config_text)
    mutate(value)

    with pytest.raises(ConfigError, match=message):
        parse_config(value)


def test_rejects_missing_required_sections(config_text: str) -> None:
    value = yaml.safe_load(config_text)
    del value["audit"]

    with pytest.raises(ConfigError, match="audit is required"):
        parse_config(value)


def test_accepts_go_duration_syntax_and_zero_request_timeout(config_text: str) -> None:
    value = yaml.safe_load(config_text)
    value["limits"]["catalog_refresh_interval"] = "1m30s"
    value["limits"]["request_timeout"] = "0s"

    config = parse_config(value)

    assert config.limits.catalog_refresh_interval_seconds == 90.0
    assert config.limits.request_timeout_seconds == DEFAULT_REQUEST_TIMEOUT_SECONDS


def test_rejects_multiple_documents(tmp_path, config_text: str) -> None:
    path = tmp_path / "conduit.yaml"
    path.write_text(config_text + "\n---\n{}\n")

    with pytest.raises(ConfigError, match="exactly one YAML document"):
        load_config(path)


def test_rejects_duplicate_yaml_key(tmp_path, config_text: str) -> None:
    path = tmp_path / "conduit.yaml"
    path.write_text(config_text.replace("  address: 127.0.0.1:8080", "  address: 127.0.0.1:8080\n  address: 127.0.0.1:8081"))

    with pytest.raises(ConfigError, match="duplicate key"):
        load_config(path)
