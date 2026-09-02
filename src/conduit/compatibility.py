"""Small, stateful compatibility boundary for standard Streamable HTTP MCP."""

from __future__ import annotations

import secrets
from dataclasses import dataclass


@dataclass(frozen=True)
class ClientSession:
    """The protocol version selected during a client's initialize request."""

    protocol_version: str


class CompatibilitySessions:
    """Own opaque client session IDs without sharing downstream state.

    These sessions only represent the public client connection.  Downstream
    session ownership remains entirely inside :mod:`conduit.transport`.
    """

    def __init__(self) -> None:
        self._sessions: dict[str, ClientSession] = {}

    def create(self, protocol_version: str) -> str:
        session_id = secrets.token_urlsafe(24)
        self._sessions[session_id] = ClientSession(protocol_version=protocol_version)
        return session_id

    def valid(self, session_id: str | None, protocol_version: str | None) -> bool:
        if not session_id or not protocol_version:
            return False
        session = self._sessions.get(session_id)
        return session is not None and session.protocol_version == protocol_version

    def remove(self, session_id: str) -> None:
        self._sessions.pop(session_id, None)

    @property
    def count(self) -> int:
        """Expose a testable cleanup invariant without exposing the tokens."""

        return len(self._sessions)
