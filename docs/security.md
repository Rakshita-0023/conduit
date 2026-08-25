# Security model

Conduit is a local-first MCP federation and enforcement boundary. It is not an
identity system, OAuth broker, approval service, or general HTTP proxy.

## Boundaries

- The listener is restricted to loopback addresses.
- Origins are absent-or-explicitly-allowed loopback origins.
- Only MCP 2026-07-28 Streamable HTTP is admitted.
- Public names resolve through stored exact routes; they are never split to
  reconstruct a downstream tool.
- Deny rules take precedence, default policy is deny, and authorization is
  rechecked against the aggregate generation.
- A durable authorization event is recorded before downstream transport.
- Client headers, cookies, and credentials are isolated from downstreams.
- Redirects, per-call discovery, initialization, and automatic tool-call
  retries are disabled.

## Operational controls

Protect audit files and downstream configuration. Use least-privilege
downstream credentials, one downstream header map per downstream, and a local
edge proxy if network exposure is needed. See [SECURITY.md](../SECURITY.md)
for vulnerability reporting.
