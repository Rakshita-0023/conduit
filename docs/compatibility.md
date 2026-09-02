# MCP compatibility

Conduit supports MCP **2026-07-28** with Streamable HTTP downstreams only.
Supported operations are `server/discover`, `tools/list`, and terminal JSON
`tools/call`. Request-scoped SSE/progress, stdio, legacy negotiation, prompts,
resources, tasks, and subscriptions are unsupported.

Stateful downstream tool calls may return `Mcp-Session-Id`; Conduit performs at
most one bounded cleanup `DELETE` using that invocation-owned session ID.
It does not retain or replay that identifier on a later invocation, so a
stateful downstream must establish each tool-call session without an earlier
client-owned session. A session advertised before a failed, oversized, or
disconnected response is still cleaned up where the downstream made that
identifier available.

An SSE response, including a progress/event stream, is not a successful tool
result: Conduit returns its documented unsupported-response error and performs
no tool-call retry. A malformed, disconnected, or oversized body after
dispatch has an uncertain outcome and is likewise never retried.
