from __future__ import annotations

import asyncio

import pytest

from conduit.policy import Policy
from conduit.registry import COLLISION, OVER_LIMIT, READY, Registry


def _tool(name: str) -> dict[str, object]:
    return {"name": name, "description": name, "inputSchema": {"type": "object"}}


@pytest.mark.asyncio
async def test_registry_orders_public_tools_and_keeps_exact_routes(config) -> None:
    registry = Registry(config.limits, Policy(config.policy), "test")
    await registry.publish("example", [_tool("z"), _tool("a")])
    snapshot = registry.snapshot
    assert snapshot.state == READY
    assert [tool["name"] for tool in snapshot.tools] == ["example.a", "example.z"]
    prepared = await registry.prepare("example.z")
    assert prepared.route.server_id == "example"
    assert prepared.route.downstream_tool_name == "z"


@pytest.mark.asyncio
async def test_deny_wins_and_default_deny(config) -> None:
    from dataclasses import replace
    from conduit.config import PolicyConfig

    blocked = replace(config, policy=PolicyConfig(("example.*",), ("example.hidden",)))
    registry = Registry(blocked.limits, Policy(blocked.policy), "test")
    await registry.publish("example", [_tool("allowed"), _tool("hidden")])
    assert [tool["name"] for tool in registry.snapshot.tools] == ["example.allowed"]


@pytest.mark.asyncio
async def test_collision_and_limit_remove_all_advertised_tools(config) -> None:
    from dataclasses import replace
    from conduit.config import LimitsConfig

    registry = Registry(config.limits, Policy(config.policy), "test")
    await registry.publish("example", [_tool("same")])
    await registry.publish("other", [_tool("same")])
    assert registry.snapshot.state == READY  # Namespaces make separate downstream names safe.
    limited = replace(config.limits, max_aggregate_tools=1)
    registry = Registry(limited, Policy(config.policy), "test")
    await registry.publish("example", [_tool("a"), _tool("b")])
    assert registry.snapshot.state == OVER_LIMIT


@pytest.mark.asyncio
async def test_authorization_holds_generation_through_durable_callback(config) -> None:
    registry = Registry(config.limits, Policy(config.policy), "test")
    await registry.publish("example", [_tool("a")])
    prepared = await registry.prepare("example.a")
    seen: list[str] = []

    async def commit() -> None:
        seen.append("durable")

    await registry.authorize(prepared, commit)
    assert seen == ["durable"]
