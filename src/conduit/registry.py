"""Immutable aggregate generations and explicit public tool routes."""

from __future__ import annotations

import asyncio
import json
from dataclasses import dataclass
from types import MappingProxyType
from typing import Any, Awaitable, Callable, Mapping

from .config import LimitsConfig
from .policy import Policy

STARTING = "starting"
READY = "ready"
UNAVAILABLE = "unavailable"
COLLISION = "aggregate_collision"
OVER_LIMIT = "aggregate_over_limit"


@dataclass(frozen=True)
class Route:
    """A complete downstream destination; public names are never split."""

    public_name: str
    server_id: str
    downstream_tool_name: str


@dataclass(frozen=True)
class Snapshot:
    generation: int
    state: str
    tools: tuple[Mapping[str, Any], ...]
    routes: Mapping[str, Route]
    execution: Mapping[str, tuple[Route, Mapping[str, Any]]]
    result: Mapping[str, Any] | None

    @property
    def tool_count(self) -> int:
        return len(self.tools)


@dataclass(frozen=True)
class PreparedRoute:
    generation: int
    route: Route
    tool: Mapping[str, Any]
    policy_digest: str


class RouteMissing(LookupError):
    pass


class RouteChanged(RuntimeError):
    pass


class RouteDenied(PermissionError):
    pass


class Registry:
    """Own complete downstream catalogs and publish only finished snapshots."""

    def __init__(self, limits: LimitsConfig, policy: Policy, build_version: str) -> None:
        self._limits = limits
        self._policy = policy
        self._build_version = build_version
        self._catalogs: dict[str, tuple[Mapping[str, Any], ...]] = {}
        self._snapshot = Snapshot(0, STARTING, (), MappingProxyType({}), MappingProxyType({}), None)
        self._lock = asyncio.Lock()

    @property
    def snapshot(self) -> Snapshot:
        return self._snapshot

    async def publish(self, server_id: str, tools: list[Mapping[str, Any]]) -> Snapshot:
        owned = tuple(_frozen_json(tool) for tool in tools)
        async with self._lock:
            self._catalogs[server_id] = owned
            self._rebuild()
            return self._snapshot

    async def remove(self, server_id: str) -> Snapshot:
        async with self._lock:
            self._catalogs.pop(server_id, None)
            self._rebuild()
            return self._snapshot

    async def prepare(self, public_name: str) -> PreparedRoute:
        async with self._lock:
            if self._snapshot.state != READY or public_name not in self._snapshot.execution:
                raise RouteMissing(public_name)
            route, tool = self._snapshot.execution[public_name]
            return PreparedRoute(self._snapshot.generation, route, _frozen_json(tool), self._policy.digest)

    async def authorize(self, prepared: PreparedRoute, commit: Callable[[], Awaitable[None]]) -> None:
        """Hold publication while the caller durably records authorization."""

        async with self._lock:
            active = self._snapshot
            entry = active.execution.get(prepared.route.public_name)
            if active.state != READY or active.generation != prepared.generation or entry is None or entry[0] != prepared.route:
                raise RouteChanged(prepared.route.public_name)
            if not self._policy.allowed(prepared.route.public_name):
                raise RouteDenied(prepared.route.public_name)
            await commit()

    def _rebuild(self) -> None:
        generation = self._snapshot.generation + 1
        if not self._catalogs:
            self._snapshot = Snapshot(generation, UNAVAILABLE, (), MappingProxyType({}), MappingProxyType({}), None)
            return
        all_tools: dict[str, tuple[Mapping[str, Any], Mapping[str, Any], Route]] = {}
        for server_id, catalog in self._catalogs.items():
            for original in catalog:
                name = original.get("name") if isinstance(original, Mapping) else None
                if not isinstance(name, str) or not name:
                    self._snapshot = Snapshot(generation, UNAVAILABLE, (), MappingProxyType({}), MappingProxyType({}), None)
                    return
                public_name = f"{server_id}.{name}"
                if public_name in all_tools:
                    self._snapshot = Snapshot(generation, COLLISION, (), MappingProxyType({}), MappingProxyType({}), None)
                    return
                public = _thaw_json(original)
                public["name"] = public_name
                all_tools[public_name] = (_frozen_json(public), original, Route(public_name, server_id, name))
        names = sorted(name for name in all_tools if self._policy.allowed(name))
        if len(names) > self._limits.max_aggregate_tools:
            self._snapshot = Snapshot(generation, OVER_LIMIT, (), MappingProxyType({}), MappingProxyType({}), None)
            return
        tools = tuple(all_tools[name][0] for name in names)
        routes = MappingProxyType({name: all_tools[name][2] for name in names})
        execution = MappingProxyType({name: (entry[2], entry[1]) for name, entry in all_tools.items()})
        result = {
            "resultType": "complete",
            "_meta": {"io.modelcontextprotocol/serverInfo": {"name": "conduit", "version": self._build_version}},
            "ttlMs": 0,
            "cacheScope": "public",
            "tools": [_thaw_json(tool) for tool in tools],
        }
        if len(json.dumps(result, separators=(",", ":"), ensure_ascii=False).encode()) > self._limits.max_aggregate_response_bytes:
            self._snapshot = Snapshot(generation, OVER_LIMIT, (), MappingProxyType({}), MappingProxyType({}), None)
            return
        self._snapshot = Snapshot(generation, READY, tools, routes, execution, _frozen_json(result))


def _frozen_json(value: Mapping[str, Any]) -> Mapping[str, Any]:
    """Recursively detach JSON data before publishing a generation."""

    frozen = _freeze(value)
    if not isinstance(frozen, Mapping):  # defensive: tools and results are objects
        raise TypeError("JSON object expected")
    return frozen


def _thaw_json(value: Mapping[str, Any]) -> dict[str, Any]:
    thawed = _thaw(value)
    if not isinstance(thawed, dict):  # defensive: tools and results are objects
        raise TypeError("JSON object expected")
    return thawed


def _freeze(value: Any) -> Any:
    if isinstance(value, Mapping):
        return MappingProxyType({str(key): _freeze(item) for key, item in value.items()})
    if isinstance(value, list) or isinstance(value, tuple):
        return tuple(_freeze(item) for item in value)
    return value


def _thaw(value: Any) -> Any:
    if isinstance(value, Mapping):
        return {str(key): _thaw(item) for key, item in value.items()}
    if isinstance(value, tuple):
        return [_thaw(item) for item in value]
    return value
