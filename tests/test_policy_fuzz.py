"""Adversarial policy and route identity tests with deterministic fuzzing."""

from __future__ import annotations

from dataclasses import replace

import pytest
import yaml
from hypothesis import HealthCheck, given, settings
from hypothesis import strategies as st

from conduit.config import ConfigError, PolicyConfig, parse_config, valid_downstream_tool_name
from conduit.policy import Policy
from conduit.registry import READY, UNAVAILABLE, Registry, RouteDenied, RouteMissing

ADVERSARIAL_PUBLIC_NAMES = [
    "github..delete",
    ".github.delete",
    "github.delete.",
    "github/delete",
    "github\\delete",
    "github:delete",
    "github delete",
    "github\tdelete",
    "github\ndelete",
    " github.delete",
    "github.delete ",
    "GitHub.delete",
    "GITHUB.DELETE",
    "github.*",
    "*",
    "*.*",
    "github.",
    "github...delete",
    "github\uff0edelete",
    "github\u2024delete",
    "github\u200bdelete",
    "g\u0430ithub.delete",
    "github\u00a0delete",
    "github\x00delete",
    "github\rdelete",
    "../github.delete",
    "github../delete",
    "github.%2e.delete",
    'github."delete"',
    "github." + "x" * 129,
]


def _tool(name: str) -> dict[str, object]:
    return {"name": name, "inputSchema": {"type": "object"}}


@pytest.mark.asyncio
@pytest.mark.parametrize("raw_name", ADVERSARIAL_PUBLIC_NAMES)
async def test_malformed_downstream_names_are_never_published_or_routable(config, raw_name: str) -> None:
    registry = Registry(config.limits, Policy(config.policy), "test")
    await registry.publish("github", [_tool(raw_name)])
    assert registry.snapshot.state == UNAVAILABLE
    with pytest.raises(RouteMissing):
        await registry.prepare(f"github.{raw_name}")


@pytest.mark.asyncio
async def test_empty_and_unknown_public_names_never_authorize(config) -> None:
    registry = Registry(config.limits, Policy(config.policy), "test")
    await registry.publish("github", [_tool("delete")])
    for name in ("", "github", "github.", "github..delete", "linear.delete"):
        with pytest.raises(RouteMissing):
            await registry.prepare(name)


@pytest.mark.asyncio
async def test_namespace_identity_is_exact_and_cannot_impersonate_another_server(config) -> None:
    policy = Policy(PolicyConfig(("github.*", "linear.*"), ("linear.delete",)))
    registry = Registry(config.limits, policy, "test")
    await registry.publish("github", [_tool("linear-delete"), _tool("delete")])
    await registry.publish("linear", [_tool("delete")])

    github = await registry.prepare("github.linear-delete")
    assert github.route.server_id == "github"
    assert github.route.downstream_tool_name == "linear-delete"
    # A policy decision on linear.delete cannot authorize github.delete and
    # vice versa; routes are stored exact values, never parsed by separator.
    await registry.authorize(github, _no_op)
    with pytest.raises(RouteDenied):
        await registry.authorize(await registry.prepare("linear.delete"), _no_op)
    with pytest.raises(RouteMissing):
        await registry.prepare("linear.github-delete")


async def _no_op() -> None:
    return


def test_policy_deny_default_deny_wildcards_and_case_are_literal() -> None:
    policy = Policy(PolicyConfig(("github.*", "GitHub.delete"), ("github.delete",)))
    assert policy.allowed("github.search")
    assert not policy.allowed("github.delete")  # deny always wins
    assert policy.allowed("GitHub.delete")
    assert not policy.allowed("GITHUB.DELETE")
    assert not policy.allowed("linear.search")  # default deny
    assert not policy.allowed("githubish.search")


@pytest.mark.parametrize(
    "rule",
    ["", "*", "*.*", "github.*.*", "github..delete", ".github.delete", "github.delete.", "github/delete", "github delete", "github\tdelete", "github\ndelete", "github.%2e.delete", "github." + "x" * 129],
)
def test_invalid_policy_patterns_fail_at_configuration_load(config_text: str, rule: str) -> None:
    value = yaml.safe_load(config_text)
    value["policy"]["allow"] = [rule]
    with pytest.raises(ConfigError, match="policy rules"):
        parse_config(value)


@pytest.mark.asyncio
@settings(max_examples=300, derandomize=True, database=None, suppress_health_check=[HealthCheck.function_scoped_fixture])
@given(raw_name=st.text(min_size=0, max_size=180))
async def test_catalog_name_fuzz_has_no_ambiguous_or_routable_invalid_identity(config, raw_name: str) -> None:
    registry = Registry(config.limits, Policy(config.policy), "test")
    await registry.publish("github", [_tool(raw_name)])
    public_name = f"github.{raw_name}"
    if valid_downstream_tool_name(raw_name):
        assert registry.snapshot.state == READY
        prepared = await registry.prepare(public_name)
        assert prepared.route.public_name == public_name
        assert prepared.route.server_id == "github"
        assert prepared.route.downstream_tool_name == raw_name
    else:
        assert registry.snapshot.state == UNAVAILABLE
        with pytest.raises(RouteMissing):
            await registry.prepare(public_name)


@settings(max_examples=300, derandomize=True, database=None)
@given(
    allow=st.lists(st.sampled_from(["github.*", "github.search", "linear.*", "linear.search"]), max_size=4),
    deny=st.lists(st.sampled_from(["github.*", "github.search", "linear.*", "linear.search"]), max_size=4),
    name=st.sampled_from(["github.search", "github.delete", "linear.search", "linear.delete"]),
)
def test_policy_fuzz_matches_deny_wins_reference(allow: list[str], deny: list[str], name: str) -> None:
    policy = Policy(PolicyConfig(tuple(allow), tuple(deny)))

    def matches(rule: str) -> bool:
        return rule == name or (rule.endswith(".*") and name.startswith(rule[:-1]))

    assert policy.allowed(name) is (not any(matches(rule) for rule in deny) and any(matches(rule) for rule in allow))


@pytest.mark.asyncio
async def test_duplicate_raw_names_stay_in_separate_namespaces_and_policy_scopes(config) -> None:
    configured = replace(config, policy=PolicyConfig(("github.*", "linear.*"), ("github.search",)))
    registry = Registry(configured.limits, Policy(configured.policy), "test")
    await registry.publish("github", [_tool("search")])
    await registry.publish("linear", [_tool("search")])
    assert [tool["name"] for tool in registry.snapshot.tools] == ["linear.search"]
    with pytest.raises(RouteDenied):
        await registry.authorize(await registry.prepare("github.search"), _no_op)
    await registry.authorize(await registry.prepare("linear.search"), _no_op)
