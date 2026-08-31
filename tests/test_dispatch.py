from __future__ import annotations

import asyncio
import json
from dataclasses import replace
from pathlib import Path
from typing import Any

import pytest

from conduit.audit import AuditLog
from conduit.config import AuditConfig
from conduit.dispatch import Dispatcher
from conduit.errors import (
    TOOL_OUTCOME_UNKNOWN,
    TOOL_RESPONSE_UNSUPPORTED,
    TOOL_UNAVAILABLE,
    GatewayError,
    ResponseTooLarge,
)
from conduit.health import HealthState
from conduit.policy import Policy
from conduit.registry import Registry
from conduit.transport import DownstreamReply


class _Transport:
    def __init__(self, reply: DownstreamReply | Exception) -> None:
        self.reply = reply
        self.calls = 0
        self.cleanups: list[str] = []

    async def request(self, *_: object, **__: object) -> DownstreamReply:
        self.calls += 1
        if isinstance(self.reply, Exception):
            raise self.reply
        return self.reply

    async def cleanup(self, _: str, session_id: str, __: object, ___: float) -> None:
        self.cleanups.append(session_id)


async def _dispatcher(config, tmp_path: Path, reply: DownstreamReply | Exception) -> tuple[Dispatcher, _Transport, AuditLog]:
    configured = replace(config, audit=AuditConfig(str(tmp_path / "audit.jsonl")))
    registry = Registry(configured.limits, Policy(configured.policy), "test")
    snapshot = await registry.publish("example", [{"name": "run", "inputSchema": {"type": "object"}}])
    health = HealthState(configured)
    health.set_live(True)
    health.set_catalog("example", "healthy", 1)
    health.set_aggregate(snapshot)
    audit = AuditLog(configured.audit.path)
    await audit.ready()
    transport = _Transport(reply)
    return Dispatcher(configured, registry, audit, health, transport, "test"), transport, audit


def _reply(result: dict[str, Any] | None = None, *, error: dict[str, Any] | None = None, session: str | None = None, sse: bool = False) -> DownstreamReply:
    response: dict[str, Any] = {"jsonrpc": "2.0", "id": "ignored"}
    response["result" if error is None else "error"] = result if error is None else error
    return DownstreamReply(200, {}, json.dumps(response).encode(), session, sse)


@pytest.mark.asyncio
async def test_success_is_one_shot_durably_authorized_and_cleans_its_session(config, tmp_path: Path) -> None:
    dispatcher, transport, audit = await _dispatcher(config, tmp_path, _reply({"content": [], "_meta": {"preserved": True}}, session="owned"))
    # The real transport correlates an opaque id. Make the fixture accept the
    # generated id while retaining the public result behavior under test.
    original = transport.request

    async def correlated(*args: object, **kwargs: object) -> DownstreamReply:
        request_id = args[2]
        reply = await original(*args, **kwargs)
        return replace(reply, body=json.dumps({"jsonrpc": "2.0", "id": request_id, "result": {"content": [], "_meta": {"preserved": True}}}).encode())

    transport.request = correlated  # type: ignore[method-assign]
    assert (await dispatcher.execute("example.run", {}, None, None))["result"]["_meta"] == {"preserved": True}
    assert transport.calls == 1
    assert transport.cleanups == ["owned"]
    records = (tmp_path / "audit.jsonl").read_text(encoding="utf-8")
    assert records.index("tool_call_authorized") < records.index("tool_call_completed")
    await audit.close()


@pytest.mark.asyncio
async def test_downstream_error_fidelity_and_unknown_after_dispatch(config, tmp_path: Path) -> None:
    dispatcher, transport, audit = await _dispatcher(config, tmp_path, _reply(error={"code": -32602, "message": "bad parameters", "data": {"field": "x"}}))
    original = transport.request

    async def correlated(*args: object, **kwargs: object) -> DownstreamReply:
        reply = await original(*args, **kwargs)
        return replace(reply, body=json.dumps({"jsonrpc": "2.0", "id": args[2], "error": {"code": -32602, "message": "bad parameters", "data": {"field": "x"}}}).encode())

    transport.request = correlated  # type: ignore[method-assign]
    assert (await dispatcher.execute("example.run", {}, None, None))["error"]["message"] == "bad parameters"
    assert transport.calls == 1
    await audit.close()

    dispatcher, transport, audit = await _dispatcher(config, tmp_path, ResponseTooLarge("large"))
    with pytest.raises(GatewayError) as error:
        await dispatcher.execute("example.run", {}, None, None)
    assert error.value.code == TOOL_OUTCOME_UNKNOWN
    assert transport.calls == 1
    await audit.close()


@pytest.mark.asyncio
async def test_sse_is_rejected_but_its_owned_session_is_cleaned(config, tmp_path: Path) -> None:
    dispatcher, transport, audit = await _dispatcher(config, tmp_path, _reply({"content": []}, session="sse-session", sse=True))
    with pytest.raises(GatewayError) as error:
        await dispatcher.execute("example.run", {}, None, None)
    assert error.value.code == TOOL_RESPONSE_UNSUPPORTED
    assert transport.calls == 1
    assert transport.cleanups == ["sse-session"]
    await audit.close()


@pytest.mark.asyncio
async def test_shutdown_rejects_new_dispatch_without_transport(config, tmp_path: Path) -> None:
    dispatcher, transport, audit = await _dispatcher(config, tmp_path, _reply({"content": []}))
    await dispatcher.begin_shutdown()
    with pytest.raises(GatewayError) as error:
        await dispatcher.execute("example.run", {}, None, None)
    assert error.value.code == TOOL_UNAVAILABLE
    assert transport.calls == 0
    await audit.close()


@pytest.mark.asyncio
async def test_shutdown_cancels_active_dispatch_as_unknown_after_dispatch(config, tmp_path: Path) -> None:
    dispatcher, transport, audit = await _dispatcher(config, tmp_path, _reply({"content": []}))
    started = asyncio.Event()
    never = asyncio.Event()

    async def blocking(*_: object, **__: object) -> DownstreamReply:
        transport.calls += 1
        started.set()
        await never.wait()
        raise AssertionError("blocking request unexpectedly resumed")

    transport.request = blocking  # type: ignore[method-assign]
    active = asyncio.create_task(dispatcher.execute("example.run", {}, None, None))
    await started.wait()
    await dispatcher.cancel_active()
    with pytest.raises(GatewayError) as error:
        await active
    assert error.value.code == TOOL_OUTCOME_UNKNOWN
    await dispatcher.wait()
    assert "tool_call_unknown_after_dispatch" in (tmp_path / "audit.jsonl").read_text(encoding="utf-8")
    await audit.close()
