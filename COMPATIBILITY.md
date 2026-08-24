# Conduit v0.1 compatibility

Conduit supports **MCP 2026-07-28 only**. It accepts and connects to
Streamable HTTP MCP endpoints only; there is no stdio support and no legacy
protocol fallback.

The supported public subset is `server/discover`, aggregate `tools/list`, and
terminal JSON `tools/call`. Discovery reports only this tools capability.
`tools/list` returns policy-filtered, deterministic namespaced tools. A
`tools/call` is dispatched to the exact discovered downstream route after
authorization; it is not a general downstream method proxy.

For calls, Conduit supports normal complete results, `isError` results, raw
structured content and `_meta`, correlated downstream JSON-RPC errors, and
`input_required` only when it includes `requestState` or an empty
`inputRequests` object. A follow-up call is independently authorized.
Capability-requiring input requests are unsupported.

Conduit does not automatically retry or replay `tools/call`. A malformed,
oversized, truncated, unsupported, or transport-uncertain post-dispatch result
is reported conservatively as an uncertain outcome.

Request-scoped downstream SSE/progress is unsupported. A downstream
`text/event-stream` tool response is rejected after dispatch; progress is not
consumed, relayed, or silently dropped. If such a response creates a
Streamable HTTP session, Conduit still performs at most one invocation-owned,
bounded cleanup `DELETE`. A terminal JSON response with an invocation-owned
session ID receives the same cleanup; stateless calls receive no cleanup.

Prompts, resources, tasks, subscriptions, OAuth brokerage, identity/users,
approvals, database-backed features, dashboard functionality, and legacy MCP
capabilities are not supported in v0.1.
