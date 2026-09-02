# MCP compatibility

Conduit has one policy, audit, registry, and dispatcher. Its native public
profile is MCP **2026-07-28** (`server/discover`, `tools/list`, and terminal
JSON `tools/call`). At ingress only, it also adapts the standard session-based
Streamable HTTP negotiation used by current desktop and CLI MCP clients. The
adapter never changes the modern protocol used to communicate with downstream
servers.

## Compatibility matrix

| Client / mode | Status | Negotiation accepted | Last verified |
| --- | --- | --- | --- |
| MCP Inspector 2.4.0, default | Supported | `initialize` + session, `2025-11-25` | Real `tools/list` and `tools/call` through Conduit on 2026-09-02. |
| MCP Inspector 2.4.0, `protocolEra: modern` | Supported | Native `server/discover`, `2026-07-28` | Real `tools/list` and `tools/call` on 2026-09-02. |
| Claude Code 2.1.236, HTTP MCP | Supported | Standard session-based Streamable HTTP (`2025-11-25`) | `claude mcp get` reported `Connected` on 2026-09-02. An authenticated Claude conversation is required to issue a tool call. |
| Codex CLI 0.149.1 remote MCP | Supported | Standard session-based Streamable HTTP (`2025-06-18`) | The real Codex connector completed MCP initialization/event-channel setup on 2026-09-02. A logged-in Codex session is required for model-directed tool calls. |
| Older/other standard clients | Conditional | Exactly `2025-06-18` or `2025-11-25`, `initialize`, `Mcp-Session-Id` | Try the standard Streamable HTTP endpoint; unsupported protocol versions fail closed. |
| stdio, SSE-only, OAuth broker clients | Unsupported | — | Conduit is an HTTP gateway, not a stdio/SSE/OAuth broker. |

The verification dates describe installed-client interoperability, not a claim
that a client vendor will never change its wire behavior. Keep Conduit and the
client updated together and use the regression tests when upgrading either.

## What the adapter accepts

For a standard client, Conduit accepts `initialize` with protocol version
`2025-06-18` or `2025-11-25`, returns an opaque `Mcp-Session-Id`, accepts the
client's `notifications/initialized`, and requires that same session/version
pair on later `tools/list` and `tools/call` requests. Sessions are opaque,
per-client, version-bound, and removed by `DELETE /mcp`; they are never passed
to a downstream or used to select a route.

`GET /mcp` for a valid standard session returns a short comment-only
`text/event-stream` response. It exists for clients that establish their
notification channel during initialization. Conduit never turns a downstream
tool response into SSE and does not emit progress, subscriptions, tasks, or
server notifications. Tool results remain terminal JSON.

All adapted `tools/list` and `tools/call` requests continue through the exact
same registry snapshot, deny-wins policy, header validation, durable
audit-before-dispatch write, credential isolation, route lookup, timeout, and
one-shot downstream dispatch as native modern requests. The adapter supplies
only the internal `Mcp-Name` correlation header from the validated JSON body;
a caller-provided conflicting value is rejected.

Stateful downstream tool calls may return `Mcp-Session-Id`; Conduit performs at
most one bounded cleanup `DELETE` using that invocation-owned downstream
session ID. It does not retain or replay that identifier on a later invocation.
A session advertised before a failed, oversized, or disconnected response is
still cleaned up where the downstream made that identifier available.

An SSE response, including a progress/event stream, is not a successful
downstream tool result: Conduit returns its documented unsupported-response
error and performs no tool-call retry. A malformed, disconnected, or oversized
body after dispatch has an uncertain outcome and is likewise never retried.

## Client setup

Start Conduit and wait for `"ready": true` first:

```sh
conduit --config conduit.yaml
curl -sS http://127.0.0.1:8080/healthz
```

### Claude Code

```sh
claude mcp add --transport http conduit http://127.0.0.1:8080/mcp
claude mcp list
```

For project configuration use `--scope project`; Claude Code requires project
MCP approval before it opens the connection. `claude mcp get conduit` should
show `Status: Connected`. Then use the namespaced tools in an authenticated
Claude Code conversation, for example `github.add` or `calc.square`.

### Codex CLI / Codex app

```sh
codex mcp add conduit --url http://127.0.0.1:8080/mcp
codex mcp list
```

Start a logged-in Codex session and ask it to use a namespaced Conduit tool.
The remote MCP configuration is HTTP; do not configure Conduit as a stdio
server. A 401 from the OpenAI model endpoint is Codex authentication failure,
not a Conduit MCP protocol failure.

### MCP Inspector default mode

```sh
npx @modelcontextprotocol/inspector --cli \
  --transport http --server-url http://127.0.0.1:8080/mcp \
  --method tools/list --format json
```

To call a tool:

```sh
npx @modelcontextprotocol/inspector --cli \
  --transport http --server-url http://127.0.0.1:8080/mcp \
  --method tools/call --tool-name calc.square \
  --tool-args-json '{"value": 9}' --format json
```

### MCP Inspector forced-modern mode

Save `inspector-modern.json`:

```json
{
  "mcpServers": {
    "conduit": {
      "type": "http",
      "url": "http://127.0.0.1:8080/mcp",
      "protocolEra": "modern"
    }
  }
}
```

Then run:

```sh
npx @modelcontextprotocol/inspector --cli \
  --config inspector-modern.json --server conduit \
  --method tools/list --format json
```
