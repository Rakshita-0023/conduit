"""Event-loop-owned public liveness, readiness, and degradation state."""

from __future__ import annotations

from datetime import UTC, datetime

from .config import Config
from .registry import Snapshot


class HealthState:
    """Own mutable status only on the app event loop; return detached JSON."""

    def __init__(self, config: Config) -> None:
        self._live = False
        self._audit_healthy = True
        self._attempted = {item.id: False for item in config.downstreams}
        self._downstreams = {item.id: {"id": item.id, "state": "starting", "tool_count": 0} for item in config.downstreams}
        self._aggregate: dict[str, int | str] = {"generation": 0, "state": "starting", "tool_count": 0}

    def set_live(self, value: bool) -> None:
        self._live = value

    def set_audit(self, value: bool) -> None:
        self._audit_healthy = value

    def set_catalog(self, server_id: str, state: str, tools: int, error: str = "") -> None:
        item = {"id": server_id, "state": state, "tool_count": tools}
        self._attempted[server_id] = True
        if state == "healthy":
            item["last_success"] = datetime.now(UTC).isoformat().replace("+00:00", "Z")
        if error:
            item["error"] = error
        self._downstreams[server_id] = item

    def set_aggregate(self, snapshot: Snapshot) -> None:
        if snapshot.generation >= int(self._aggregate["generation"]):
            self._aggregate = {"generation": snapshot.generation, "state": snapshot.state, "tool_count": snapshot.tool_count}

    def snapshot(self) -> dict[str, object]:
        downstreams = [dict(self._downstreams[key]) for key in sorted(self._downstreams)]
        ready = self._live and self._audit_healthy and all(self._attempted.values()) and any(item["state"] == "healthy" for item in downstreams) and self._aggregate["state"] == "ready"
        return {"live": self._live, "ready": ready, "audit_healthy": self._audit_healthy, "aggregate": dict(self._aggregate), "downstreams": downstreams}
