# Conduit v0.1 compatibility

Conduit supports MCP 2026-07-28 over Streamable HTTP for `server/discover`,
`tools/list`, and terminal JSON `tools/call` responses.

For tool calls, Conduit supports normal complete results, `isError` results,
raw structured content and `_meta`, downstream JSON-RPC errors, and
`input_required` only when it has `requestState` or an empty `inputRequests`
object; capability-requiring input requests are unsupported. A follow-up call
is an independently authorized invocation.

Request-scoped downstream SSE/progress, prompts, resources, Tasks,
subscriptions, legacy MCP versions, OAuth brokerage, and stdio are not
supported. A downstream SSE/progress response is rejected after dispatch and
reported as an uncertain outcome; Conduit does not silently drop it.
