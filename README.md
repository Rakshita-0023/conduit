# Conduit

[![CI](https://github.com/Rakshita-0023/conduit/actions/workflows/ci.yml/badge.svg)](https://github.com/Rakshita-0023/conduit/actions/workflows/ci.yml)

Conduit is a deterministic, local-first MCP gateway that federates tools from
multiple Streamable HTTP servers behind one policy-enforced and auditable
endpoint.

It presents one MCP 2026-07-28 endpoint, publishes stable names such as
`github.search_code`, and routes an authorised call only through its stored
downstream route. It is deliberately not a general-purpose MCP proxy.

## Install

The Python distribution name is **`conduit-gateway`** (the import and command
remain `conduit`):

```sh
pipx install conduit-gateway
# or: python -m pip install conduit-gateway
```

For local development from a checkout:

```sh
python -m venv .venv
. .venv/bin/activate
python -m pip install -e '.[test]'
```

The Go v0.1.x releases remain available through their existing release tags and
the `go-v0.1-maintenance` branch; this branch is the Python implementation.

## Quick start

```sh
conduit --init --config conduit.yaml
# Edit the downstream URL, its owned credentials, and policy rules.
conduit --config conduit.yaml
```

`--init` writes the bundled template privately and never overwrites an existing
configuration. Checkout users may also copy the root `conduit.example.yaml`.

Conduit binds loopback addresses only. Once its downstream catalog is ready:

```sh
curl http://127.0.0.1:8080/healthz
```

```yaml
listener:
  address: 127.0.0.1:8080
  allowed_origins: []
audit:
  path: ./conduit-audit.jsonl
policy:
  allow: ["github.*"]
  deny: []
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

## Scope and safety model

Conduit supports `server/discover`, deterministic policy-filtered `tools/list`,
and terminal JSON `tools/call` over MCP **2026-07-28**. Downstreams are
Streamable HTTP only. It has no automatic tool-call retries, legacy fallback,
stdio, OAuth broker, identity system, database, dashboard, or SSE/progress
bridge.

Every public tool has an explicit immutable route. Policy is deny-wins, then
allow, then default-deny. A durable `tool_call_authorized` audit entry is
fsynced before downstream side effects. Caller headers, cookies, and
credentials are never forwarded; configured downstream headers are isolated per
server. Redirects are disabled, response bodies are bounded while streaming,
and downstream sessions receive a single invocation-owned cleanup `DELETE`.

```mermaid
flowchart LR
  C[MCP client] --> I[Conduit ingress]
  I --> R[Immutable registry + policy]
  R --> A[Durable audit]
  A --> D[One-shot dispatcher]
  D --> S[Exact downstream MCP route]
```

Read [the documentation](https://rakshita-0023.github.io/conduit/) for
configuration, operations, protocol compatibility, security, and development.

## Development

```sh
python -m pytest --cov
ruff check .
mypy src
python -m build
```

See [CONTRIBUTING.md](CONTRIBUTING.md), [SECURITY.md](SECURITY.md), and the
[Apache-2.0 license](LICENSE).
