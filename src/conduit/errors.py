"""Public error codes and internal exceptions for the Conduit gateway."""

from __future__ import annotations

from dataclasses import dataclass

TOOL_UNAVAILABLE = -32010
TOOL_DISPATCH_FAILED = -32011
TOOL_OUTCOME_UNKNOWN = -32012
TOOL_RESPONSE_UNSUPPORTED = -32013
AUDIT_UNAVAILABLE = -32014


@dataclass(frozen=True)
class GatewayError(Exception):
    """An implementation-defined MCP error that is safe to expose."""

    code: int
    message: str
    data: object | None = None


class AuditUnavailable(RuntimeError):
    """The durable audit boundary cannot accept another event."""


class ResponseTooLarge(RuntimeError):
    """A bounded downstream response exceeded its configured limit."""


class UnsupportedResponse(RuntimeError):
    """A downstream response is valid HTTP but outside v0.1 support."""
