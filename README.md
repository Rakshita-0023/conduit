# Conduit

> A deterministic, local-first MCP gateway for routing, policy enforcement, credential isolation, and auditable access across multiple MCP servers.

[![PyPI](https://img.shields.io/pypi/v/conduit-gateway?label=PyPI)](https://pypi.org/project/conduit-gateway/)
[![Python versions](https://img.shields.io/pypi/pyversions/conduit-gateway)](https://pypi.org/project/conduit-gateway/)
[![CI](https://github.com/Rakshita-0023/conduit/actions/workflows/ci.yml/badge.svg)](https://github.com/Rakshita-0023/conduit/actions/workflows/ci.yml)
[![Docs](https://img.shields.io/github/actions/workflow/status/Rakshita-0023/conduit/docs.yml?label=docs)](https://github.com/Rakshita-0023/conduit/actions/workflows/docs.yml)
[![License](https://img.shields.io/github/license/Rakshita-0023/conduit)](LICENSE)

## Why Conduit?

When one AI client connects directly to several MCP servers, each connection owns its own credentials, policy, naming, and audit behavior:

```text
AI client
├── GitHub MCP
├── Slack MCP
├── Database MCP
└── other MCPs
```

Conduit puts one local control layer in front of those servers:

```text
AI client
    ↓
 Conduit
    ↓
GitHub / Slack / Database / other MCPs
```

It federates the tool catalog while keeping each downstream route and credential boundary explicit.

## Key features

- Federates multiple Streamable HTTP MCP servers behind one local endpoint.
- Publishes deterministic namespaced tools such as `github.search_code`.
- Applies centralized, deny-wins policy with default-deny behavior.
- Durably records authorization before downstream side effects.
- Keeps configured downstream credentials isolated; caller credentials and cookies are not forwarded.
- Supports its native modern MCP profile and verified standard HTTP client negotiation for Codex CLI, Claude Code, and MCP Inspector.
- Accepts bounded terminal JSON or finite terminal-SSE downstream responses without becoming an SSE/progress bridge.
- Uses bounded reads, disabled redirects, explicit health/degraded/recovery states, and no automatic `tools/call` replay.
- Runs local-first: the listener is restricted to loopback addresses.

## Install

```sh
pip install conduit-gateway
```

The package command and import name are `conduit`.

## Quick start

Create a private configuration file, then start the gateway:

```sh
conduit --init --config conduit.yaml
# Edit conduit.yaml with your downstream URLs, credentials, and policy.
conduit --config conduit.yaml
```

`--init` refuses to overwrite an existing file. For a reproducible local walkthrough with safe test servers, run [`docs/demo/run-demo.sh`](docs/demo/run-demo.sh).

Here is the smallest useful shape of a configuration; all values below are local placeholders, not credentials:

```yaml
listener:
  address: 127.0.0.1:8080
  allowed_origins: []
audit:
  path: ./conduit-audit.jsonl
policy:
  allow: ["calc.*"]
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
  - id: calc
    url: http://127.0.0.1:9000/mcp
    headers: {}
```

Wait for readiness before connecting a client:

```sh
curl -sS http://127.0.0.1:8080/healthz
```

## Example configuration

Each downstream owns only its own headers. Tool names are exposed as `<downstream-id>.<tool-name>` and policy rules use the same exact or `.*` syntax.

```yaml
policy:
  allow: ["github.*", "calc.*"]
  deny: ["github.delete_*"]
downstreams:
  - id: github
    url: http://127.0.0.1:9000/mcp
    # Add only credentials owned by this downstream in your private local file.
    headers: {}
  - id: calc
    url: http://127.0.0.1:9001/mcp
    headers: {}
```

Keep real credentials only in a private local configuration file; never commit them.

## Client setup

Start Conduit and wait for `"ready": true` first.

### Codex CLI

```sh
codex mcp add conduit --url http://127.0.0.1:8080/mcp
codex mcp list
```

Conduit’s Codex transport/session setup was verified with Codex CLI 0.149.1. Model-directed calls still require an authenticated Codex session; they were not run in the unauthenticated compatibility audit environment.

### Claude Code

```sh
claude mcp add --transport http conduit http://127.0.0.1:8080/mcp
claude mcp get conduit
```

Claude Code 2.1.236 reported the HTTP connection as `Connected` in the compatibility audit. An authenticated Claude conversation is required to verify model-directed calls.

### MCP Inspector

```sh
npx @modelcontextprotocol/inspector --cli \
  --transport http --server-url http://127.0.0.1:8080/mcp \
  --method tools/list --format json
```

Inspector 2.4.0 default and forced-modern modes were verified for connection, discovery, and tool calls. See the [compatibility guide](docs/compatibility.md) for call and forced-modern commands.

## Architecture

![Conduit architecture](docs/assets/conduit-architecture.svg)

Conduit is the only client-facing endpoint. Its protocol normalization, policy, audit, catalog, exact routing, and health controls run before an isolated downstream transport call.

## Policy example

```yaml
policy:
  allow: ["server.*"]
  deny: ["server.dangerous_tool"]
```

Rules match a complete public name or a namespace wildcard. Deny wins; tools not allowed by policy are hidden from the published catalog and unavailable for calls. Names are case-sensitive and validated strictly. See the [policy reference](docs/policy.md).

## Safety guarantees

- Conduit does not automatically retry or replay a `tools/call`.
- A durable authorization record is written and fsynced before dispatch; an unavailable audit destination fails closed.
- Caller `Authorization`, cookies, and arbitrary request headers do not cross into a downstream; configured headers stay scoped to their owner.
- Catalog and tool responses are bounded while read; redirects and malformed or indefinite terminal responses fail closed.
- A response lost after dispatch is treated as an unknown outcome rather than replayed.

## Compatibility

| Client / integration | Connection | Tool discovery | Tool call | Evidence / notes |
| --- | --- | --- | --- | --- |
| MCP Inspector 2.4.0, default | Verified | Verified | Verified | Real standard-session `2025-11-25` run on 2026-09-02. |
| MCP Inspector 2.4.0, forced modern | Verified | Verified | Verified | Real native `2026-07-28` run on 2026-09-02. |
| Claude Code 2.1.236, HTTP MCP | Verified | Connection verified | Not model-directed | `claude mcp get` reported `Connected`; authenticated conversation required for a model call. |
| Codex CLI 0.149.1, remote MCP | Verified | Session setup verified | Not model-directed | Real connector initialization/event-channel setup; authenticated Codex required for a model call. |
| Official Python MCP SDK | Verified | Verified | Verified | Clean installed-wheel harness against local Streamable HTTP servers. |
| GitHub remote MCP | Verified | Verified | Verified | Read-only live call through Conduit using GitHub’s terminal-SSE response. |

The exact supported versions and limitations are maintained in the [compatibility documentation](docs/compatibility.md).

## Documentation

Read the full documentation at [rakshita-0023.github.io/conduit](https://rakshita-0023.github.io/conduit/), including [getting started](docs/getting-started.md), [configuration](docs/configuration.md), [operations](docs/operations.md), and [troubleshooting](docs/troubleshooting.md).

## Development

```sh
python -m venv .venv
. .venv/bin/activate
python -m pip install -e '.[test,docs,release]'
python -m pytest --cov
ruff check .
mypy src
mkdocs build --strict
```

## Security

See [SECURITY.md](SECURITY.md) for the vulnerability reporting process and [docs/security.md](docs/security.md) for operational guidance.

## License

Conduit is licensed under the [Apache License 2.0](LICENSE).
