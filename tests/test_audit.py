from __future__ import annotations

import os

import pytest

from conduit.audit import AuditLog
from conduit.errors import AuditUnavailable


@pytest.mark.asyncio
async def test_audit_is_durable_and_unavailable_after_close(tmp_path) -> None:
    path = tmp_path / "audit.jsonl"
    audit = AuditLog(str(path))
    await audit.ready()
    await audit.append({"event": "tool_call_authorized", "call_id": "one"})
    assert path.stat().st_mode & 0o077 == 0
    assert "tool_call_authorized" in path.read_text()
    await audit.close()
    assert not audit.available
    with pytest.raises(AuditUnavailable):
        await audit.append({"event": "after_close"})


def test_audit_rejects_group_readable_existing_file(tmp_path) -> None:
    path = tmp_path / "audit.jsonl"
    path.write_text("old\n")
    os.chmod(path, 0o644)
    with pytest.raises(AuditUnavailable):
        AuditLog(str(path))
