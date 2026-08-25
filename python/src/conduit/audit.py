"""Durable append-only audit log used as the authorization boundary."""

from __future__ import annotations

import asyncio
import json
import os
import stat
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

from .errors import AuditUnavailable


class AuditLog:
    """Serialize JSONL writes and fsync every accepted authorization event."""

    def __init__(self, path: str) -> None:
        target = Path(path)
        try:
            if target.exists() and stat.S_IMODE(target.stat().st_mode) & 0o077:
                raise AuditUnavailable("audit file permissions must not grant group or other access")
            self._file = os.fdopen(os.open(target, os.O_WRONLY | os.O_APPEND | os.O_CREAT, 0o600), "a", encoding="utf-8")
        except OSError as exc:
            raise AuditUnavailable(f"open audit: {exc}") from exc
        self._lock = asyncio.Lock()
        self._failed = False
        self._closed = False

    async def ready(self) -> None:
        await self.append({"event": "audit_ready"})

    async def append(self, event: dict[str, Any]) -> None:
        async with self._lock:
            if self._failed or self._closed:
                raise AuditUnavailable("audit unavailable")
            payload = dict(event)
            payload.setdefault("timestamp", datetime.now(UTC).isoformat().replace("+00:00", "Z"))
            try:
                self._file.write(json.dumps(payload, separators=(",", ":"), ensure_ascii=False) + "\n")
                self._file.flush()
                os.fsync(self._file.fileno())
            except (OSError, TypeError, ValueError) as exc:
                self._failed = True
                raise AuditUnavailable("audit unavailable") from exc

    @property
    def available(self) -> bool:
        return not self._failed and not self._closed

    async def close(self) -> None:
        async with self._lock:
            if not self._closed:
                self._file.close()
                self._closed = True
                self._failed = True
