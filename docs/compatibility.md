# MCP compatibility

Conduit supports MCP **2026-07-28** with Streamable HTTP downstreams only.
Supported operations are `server/discover`, `tools/list`, and terminal JSON
`tools/call`. Request-scoped SSE/progress, stdio, legacy negotiation, prompts,
resources, tasks, and subscriptions are unsupported.

Stateful downstream tool calls may return `Mcp-Session-Id`; Conduit performs at
most one bounded cleanup `DELETE` using that invocation-owned session ID.
