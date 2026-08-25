"""Application composition, refresh ownership, and orderly shutdown."""

from __future__ import annotations

from contextlib import asynccontextmanager

from starlette.applications import Starlette
from starlette.routing import Route

import asyncio
import contextlib
import secrets

import httpx

from .audit import AuditLog
from .catalog import CatalogError, refresh_catalog
from .config import Config
from .dispatch import Dispatcher
from .health import HealthState
from .ingress import healthz, mcp, status
from .policy import Policy
from .registry import Registry
from .transport import DownstreamTransport


class Runtime:
    """Own all mutable gateway state and background tasks for one process."""

    def __init__(self, config: Config, build_version: str) -> None:
        self.config = config
        self.health = HealthState(config)
        self.registry = Registry(config.limits, Policy(config.policy), build_version)
        self.audit = AuditLog(config.audit.path)
        self.transport = DownstreamTransport(config.limits.tool_call_timeout_seconds)
        self.dispatcher = Dispatcher(config, self.registry, self.audit, self.health, self.transport, build_version)
        self._tasks: list[asyncio.Task[None]] = []

    async def start(self) -> None:
        await self.audit.ready()
        self.health.set_live(True)
        self._tasks = [asyncio.create_task(self._refresh_loop(server.id), name=f"conduit-refresh-{server.id}") for server in self.config.downstreams]

    async def close(self) -> None:
        self.health.set_live(False)
        await self.dispatcher.begin_shutdown()
        await self.dispatcher.cancel_active()
        await self.dispatcher.wait()
        for task in self._tasks:
            task.cancel()
        for task in self._tasks:
            with contextlib.suppress(asyncio.CancelledError):
                await task
        await self.transport.close()
        await self.audit.close()

    async def _refresh_loop(self, server_id: str) -> None:
        downstream = next(item for item in self.config.downstreams if item.id == server_id)
        while True:
            try:
                tools = await refresh_catalog(self.transport, downstream, self.config.limits, secrets.token_urlsafe(12))
            except asyncio.CancelledError:
                raise
            except (CatalogError, ValueError, OSError, TimeoutError, httpx.HTTPError):
                snapshot = await self.registry.remove(server_id)
                self.health.set_catalog(server_id, "degraded", 0, "catalog refresh failed")
                self.health.set_aggregate(snapshot)
            else:
                snapshot = await self.registry.publish(server_id, tools)
                self.health.set_catalog(server_id, "healthy", len(tools))
                self.health.set_aggregate(snapshot)
            await asyncio.sleep(self.config.limits.catalog_refresh_interval_seconds)


def create_app(config: Config, *, build_version: str = "dev") -> Starlette:
    """Build the ASGI application without global mutable state."""

    runtime = Runtime(config, build_version)

    @asynccontextmanager
    async def lifespan(app: Starlette):
        await runtime.start()
        try:
            yield
        finally:
            await runtime.close()

    app = Starlette(
        routes=[
            Route("/healthz", healthz(runtime.health), methods=["GET"]),
            Route("/status", status(runtime.health), methods=["GET"]),
            Route("/mcp", mcp(runtime, build_version), methods=["POST"]),
        ],
        lifespan=lifespan,
    )
    app.state.conduit_health = runtime.health
    app.state.conduit_config = config
    app.state.conduit_runtime = runtime
    return app
