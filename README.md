# Conduit

Conduit exposes one MCP endpoint that federates multiple downstream MCP servers with deterministic discovery and controlled execution.

It gives an MCP client a single local endpoint while keeping tool discovery, routing, policy enforcement, credentials, and audit ownership in one place. Conduit v0.1 is an infrastructure gateway, not a general MCP proxy.

## v0.1 scope

Conduit supports MCP **2026-07-28** over Streamable HTTP. Downstreams are Streamable HTTP endpoints only. Conduit discovers their tools, publishes deterministic namespaced names such as `github.search_code`, filters them through policy, and dispatches an authorized `tools/call` only to its exact stored downstream route.

- `server/discover`, `tools/list`, and terminal JSON `tools/call` are supported.
- A durable `tool_call_authorized` audit record is written before downstream transport starts.
- Tool calls are never automatically retried or replayed. Once transport may have started, failures are reported conservatively as uncertain-after-dispatch where appropriate.
- Caller credentials and caller-supplied headers are not forwarded. Downstream-owned credentials belong in the configured downstream headers.
- A downstream-created Streamable HTTP session is owned per invocation and receives one bounded cleanup `DELETE`; stateless calls receive none.
- Readiness is true only when Conduit is live, its audit log is available, the aggregate is usable, every configured downstream has completed an initial refresh, and at least one downstream is healthy. Otherwise `/mcp` discovery/list/execution is degraded with a not-ready response while health remains inspectable.

See [COMPATIBILITY.md](COMPATIBILITY.md) for the exact protocol profile and [docs/architecture.md](docs/architecture.md) for the implemented execution model.

## Install and run

Requires Go 1.25 or newer.

```sh
git clone https://github.com/Rakshita-0023/conduit.git
cd conduit
cp config.example.yaml conduit.yaml
# Edit conduit.yaml: set downstream URLs, credentials, and policy.
go build -o conduit ./cmd/conduit
./conduit -config conduit.yaml
```

For development, `go run ./cmd/conduit -config conduit.yaml` is equivalent. The listener is intentionally restricted to loopback addresses; put any network-facing termination or access control in front of it.

`config.example.yaml` contains every current required setting. This minimal shape is sufficient for local use:

```yaml
listener:
  address: 127.0.0.1:8080
audit:
  path: ./conduit-audit.jsonl
policy:
  allow: ["github.*"]
limits:
  max_pages_per_downstream: 32
  max_tools_per_downstream: 256
  max_downstream_catalog_bytes: 1048576
  max_aggregate_tools: 512
  max_aggregate_response_bytes: 4194304
  max_tool_response_bytes: 4194304
  catalog_refresh_interval: 60s
  request_timeout: 10s
  tool_call_timeout: 30s
downstreams:
  - id: github
    url: http://127.0.0.1:9000/mcp
    headers: {}
```

Configuration parsing is strict: unknown fields and invalid values fail startup. Do not place client credentials in this file; `downstreams[].headers` is only for credentials owned by that downstream.

## Endpoints and MCP calls

- `GET /healthz` returns liveness/readiness.
- `GET /status` returns sanitized aggregate and downstream refresh status.
- `POST /mcp` is the MCP endpoint.

MCP requests require the 2026-07-28 protocol metadata and matching transport headers. For example, after Conduit is ready:

```sh
curl -sS http://127.0.0.1:8080/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2026-07-28' \
  -H 'Mcp-Method: server/discover' \
  --data '{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}'
```

Use `Mcp-Method: tools/list` with method `tools/list` to obtain the policy-filtered aggregate. A tool call additionally carries its public name in `Mcp-Name`:

```sh
curl -sS http://127.0.0.1:8080/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2026-07-28' \
  -H 'Mcp-Method: tools/call' \
  -H 'Mcp-Name: github.search_code' \
  --data '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"github.search_code","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}'
```

## Security model

Conduit is a policy and routing boundary, not an identity system. Policy allow/deny rules control the names exposed in `tools/list` and are rechecked during authorization. Routes are stored from the discovered aggregate, so Conduit does not split public names to reconstruct a route. The registry lock is released before network I/O; authorization is linearized with the generation that produced the route and a durable audit write.

Configured downstream headers may provide downstream credentials, but protected MCP routing/session headers cannot be configured. Client headers, cookies, origin, and credentials are not propagated downstream. Redirects are disabled. Audit and status output omit configured credential values.

## Limitations

v0.1 deliberately does not support stdio, OAuth brokerage, identity/users, approvals, a database, dashboard, legacy MCP, prompts, resources, tasks, subscriptions, or request-scoped SSE/progress bridging. SSE/progress from a downstream tool call is rejected rather than silently dropped. There is no legacy fallback.

## Test

```sh
gofmt -w $(rg --files -g '*.go')
go test ./... -count=1 -timeout 60s
go vet ./...
go test -race ./... -count=1 -timeout 90s
git diff --check
```
