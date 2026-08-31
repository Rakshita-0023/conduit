from __future__ import annotations

import asyncio
from dataclasses import replace

import pytest

from conduit import app
from conduit.errors import ResponseTooLarge


@pytest.mark.asyncio
async def test_catalog_response_overflow_degrades_and_recovers_without_killing_refresh(config, tmp_path, monkeypatch) -> None:
    configured = replace(
        config,
        audit=replace(config.audit, path=str(tmp_path / "audit.jsonl")),
        limits=replace(config.limits, catalog_refresh_interval_seconds=0.01),
    )
    calls = 0

    async def refresh(*_: object) -> list[dict[str, object]]:
        nonlocal calls
        calls += 1
        if calls == 1:
            raise ResponseTooLarge("catalog exceeds configured bound")
        return [{"name": "recovered", "inputSchema": {"type": "object"}}]

    monkeypatch.setattr(app, "refresh_catalog", refresh)
    runtime = app.Runtime(configured, "test")
    await runtime.start()
    try:
        for _ in range(100):
            state = runtime.health.snapshot()
            if state["downstreams"][0]["state"] == "degraded":
                break
            await asyncio.sleep(0.01)
        assert state["downstreams"][0]["state"] == "degraded"
        assert state["ready"] is False
        assert not runtime._tasks[0].done()

        for _ in range(100):
            state = runtime.health.snapshot()
            if state["downstreams"][0]["state"] == "healthy":
                break
            await asyncio.sleep(0.01)
        assert state["downstreams"][0]["state"] == "healthy"
        assert state["ready"] is True
        assert runtime.registry.snapshot.state == "ready"
        assert calls >= 2
    finally:
        await runtime.close()
