# Configuration reference

Conduit loads one strict YAML document. Unknown fields, invalid types, invalid
policy rules, invalid origins, non-loopback listeners, and invalid downstream
URLs fail startup. Start from the repository's `conduit.example.yaml`.

## Top-level fields

| Field | Required | Description |
| --- | --- | --- |
| `listener.address` | no | Loopback host and port; defaults to `127.0.0.1:8080`. Only `127.0.0.1` and `::1` are accepted. |
| `listener.allowed_origins` | no | Exact allowed browser origins. Origins must be `http` or `https`, loopback, and include a port. An absent request Origin is allowed. |
| `audit.path` | yes | Private JSONL audit-log path. |
| `policy.allow` / `policy.deny` | no | Exact public tool name or trailing `.*` rules. Deny wins and the default is deny. |
| `limits` | yes | Positive catalog/aggregate/response bounds and refresh/timeout durations. |
| `downstreams` | yes | One or more Streamable HTTP MCP servers. |

## Limits

`max_pages_per_downstream`, `max_tools_per_downstream`,
`max_downstream_catalog_bytes`, `max_aggregate_tools`,
`max_aggregate_response_bytes`, and `max_tool_response_bytes` must be
positive. `catalog_refresh_interval` and `tool_call_timeout` must be positive;
`request_timeout` defaults to `10s` when omitted or zero.

## Downstreams

Each downstream requires a unique `id` and an `http` or `https` URL without
user-info. The `headers` map is for credentials owned by that downstream only.
Caller headers, cookies, and authorization never propagate downstream.

Conduit reserves MCP protocol, method, name, parameter, and session routing
headers. Do not configure `Mcp-Session-Id`, `MCP-Protocol-Version`,
`Mcp-Method`, `Mcp-Name`, or `Mcp-Param-*` as downstream headers.
