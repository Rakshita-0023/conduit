"""Fault-injection coverage for the audit authorization boundary."""

from __future__ import annotations

import asyncio
import errno
import json
from dataclasses import replace
from pathlib import Path
from typing import Any

import pytest

from conduit.audit import AuditLog
from conduit.config import AuditConfig
from conduit.dispatch import Dispatcher
from conduit.errors import AUDIT_UNAVAILABLE, TOOL_DISPATCH_FAILED, AuditUnavailable, GatewayError
from conduit.health import HealthState
from conduit.policy import Policy
from conduit.registry import Registry
from conduit.transport import DownstreamReply


class _Transport:
    def __init__(self) -> None:
        self.calls = 0
        self.called = asyncio.Event()

    async def request(self, _: str, __: str, request_id: str, ___: object, ____: object, _____: object, **______: object) -> DownstreamReply:
        self.calls += 1
        self.called.set()
        return DownstreamReply(200, {}, json.dumps({"jsonrpc": "2.0", "id": request_id, "result": {"content": []}}).encode(), None, False)

    async def cleanup(self, *_: object) -> None:
        return


class _FaultyFile:
    """Delegate descriptor operations while injecting one write-stage fault."""

    def __init__(self, wrapped: Any, fault: BaseException | None = None, *, short: bool = False, flush_fault: BaseException | None = None) -> None:
        self._wrapped = wrapped
        self._fault = fault
        self._short = short
        self._flush_fault = flush_fault

    def write(self, value: str) -> int:
        if self._fault is not None:
            raise self._fault
        if self._short:
            count = max(0, len(value) - 1)
            self._wrapped.write(value[:count])
            return count
        return self._wrapped.write(value)

    def flush(self) -> None:
        if self._flush_fault is not None:
            raise self._flush_fault
        self._wrapped.flush()

    def fileno(self) -> int:
        return self._wrapped.fileno()

    def close(self) -> None:
        self._wrapped.close()


async def _dispatcher(config: Any, tmp_path: Path) -> tuple[Dispatcher, _Transport, AuditLog]:
    configured = replace(config, audit=AuditConfig(str(tmp_path / "audit.jsonl")))
    registry = Registry(configured.limits, Policy(configured.policy), "test")
    snapshot = await registry.publish("example", [{"name": "side_effect", "inputSchema": {"type": "object"}}])
    health = HealthState(configured)
    health.set_live(True)
    health.set_catalog("example", "healthy", 1)
    health.set_aggregate(snapshot)
    audit = AuditLog(configured.audit.path)
    await audit.ready()
    transport = _Transport()
    return Dispatcher(configured, registry, audit, health, transport, "test"), transport, audit


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "fault",
    [PermissionError("denied"), OSError(errno.ENOSPC, "no space left"), OSError(errno.EIO, "I/O error")],
)
async def test_authorization_write_failure_blocks_side_effect(config: Any, tmp_path: Path, fault: BaseException) -> None:
    dispatcher, transport, audit = await _dispatcher(config, tmp_path)
    audit._file = _FaultyFile(audit._file, fault)  # type: ignore[assignment]

    with pytest.raises(GatewayError) as error:
        await dispatcher.execute("example.side_effect", {}, None, None)

    assert error.value.code == AUDIT_UNAVAILABLE
    assert transport.calls == 0
    assert not audit.available
    assert not dispatcher._health.snapshot()["audit_healthy"]  # type: ignore[attr-defined]
    await audit.close()


@pytest.mark.asyncio
async def test_deleted_audit_destination_fails_closed_before_side_effect(config: Any, tmp_path: Path) -> None:
    dispatcher, transport, audit = await _dispatcher(config, tmp_path)
    Path(audit._path).unlink()  # type: ignore[attr-defined]

    with pytest.raises(GatewayError) as error:
        await dispatcher.execute("example.side_effect", {}, None, None)

    assert error.value.code == AUDIT_UNAVAILABLE
    assert transport.calls == 0
    assert not audit.available
    await audit.close()


@pytest.mark.asyncio
async def test_short_write_and_fsync_failure_are_not_accepted_as_durable(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    path = tmp_path / "audit.jsonl"
    audit = AuditLog(str(path))
    await audit.ready()
    audit._file = _FaultyFile(audit._file, short=True)  # type: ignore[assignment]
    with pytest.raises(AuditUnavailable):
        await audit.append({"event": "short"})
    assert not audit.available
    await audit.close()

    audit = AuditLog(str(tmp_path / "flush.jsonl"))
    await audit.ready()
    audit._file = _FaultyFile(audit._file, flush_fault=OSError(errno.EIO, "flush failed"))  # type: ignore[assignment]
    with pytest.raises(AuditUnavailable):
        await audit.append({"event": "flush"})
    assert not audit.available
    await audit.close()

    audit = AuditLog(str(tmp_path / "fsync.jsonl"))
    await audit.ready()
    monkeypatch.setattr("conduit.audit.os.fsync", lambda _: (_ for _ in ()).throw(OSError(errno.EIO, "fsync failed")))
    with pytest.raises(AuditUnavailable):
        await audit.append({"event": "fsync"})
    assert not audit.available
    await audit.close()


def test_unwritable_audit_destination_fails_at_startup(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    def denied(*_: object, **__: object) -> int:
        raise PermissionError("denied")

    monkeypatch.setattr("conduit.audit.os.open", denied)
    with pytest.raises(AuditUnavailable):
        AuditLog(str(tmp_path / "unwritable.jsonl"))


@pytest.mark.asyncio
async def test_serialization_failure_is_fail_closed_and_a_fresh_runtime_recovers(tmp_path: Path) -> None:
    path = tmp_path / "audit.jsonl"
    audit = AuditLog(str(path))
    with pytest.raises(AuditUnavailable):
        await audit.append({"event": "bad", "value": object()})
    assert not audit.available
    await audit.close()

    recovered = AuditLog(str(path))
    await recovered.ready()
    assert recovered.available
    await recovered.close()


@pytest.mark.asyncio
async def test_concurrent_audit_records_are_serialized_and_a_failed_call_cannot_authorize_another(config: Any, tmp_path: Path) -> None:
    dispatcher, transport, audit = await _dispatcher(config, tmp_path)
    first = asyncio.create_task(dispatcher.execute("example.side_effect", {}, None, None))
    await transport.called.wait()

    audit._file = _FaultyFile(audit._file, OSError(errno.ENOSPC, "no space left"))  # type: ignore[assignment]
    failed = asyncio.create_task(dispatcher.execute("example.side_effect", {}, None, None))
    assert (await first)["result"] == {"content": []}
    with pytest.raises(GatewayError) as error:
        await failed
    assert error.value.code == AUDIT_UNAVAILABLE
    assert transport.calls == 1

    records = [json.loads(line) for line in (tmp_path / "audit.jsonl").read_text(encoding="utf-8").splitlines()]
    # The failed concurrent audit also makes a later terminal record fail
    # closed; it must never create an interleaved/partial JSON line or grant
    # authorization to the second side effect.
    assert [record["event"] for record in records] == ["audit_ready", "tool_call_authorized"]
    await audit.close()


@pytest.mark.asyncio
async def test_concurrent_successful_audit_records_are_complete_json_lines(tmp_path: Path) -> None:
    audit = AuditLog(str(tmp_path / "audit.jsonl"))
    await asyncio.gather(*(audit.append({"event": "parallel", "call_id": str(index)}) for index in range(40)))
    records = [json.loads(line) for line in (tmp_path / "audit.jsonl").read_text(encoding="utf-8").splitlines()]
    assert {record["call_id"] for record in records} == {str(index) for index in range(40)}
    await audit.close()


@pytest.mark.asyncio
async def test_shutdown_during_slow_authorization_never_starts_side_effect(config: Any, tmp_path: Path) -> None:
    dispatcher, transport, audit = await _dispatcher(config, tmp_path)
    original = audit.append
    entered = asyncio.Event()
    release = asyncio.Event()

    async def slow_append(event: dict[str, Any]) -> None:
        if event.get("event") == "tool_call_authorized":
            entered.set()
            await release.wait()
        await original(event)

    audit.append = slow_append  # type: ignore[method-assign]
    active = asyncio.create_task(dispatcher.execute("example.side_effect", {}, None, None))
    await entered.wait()
    await dispatcher.cancel_active()
    with pytest.raises(GatewayError) as error:
        await active
    assert error.value.code == TOOL_DISPATCH_FAILED
    assert transport.calls == 0
    await dispatcher.wait()
    assert "tool_call_authorized" not in (tmp_path / "audit.jsonl").read_text(encoding="utf-8")
    await audit.close()
