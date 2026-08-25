# Troubleshooting

## Conduit is not ready

Inspect `GET /status`. A gateway is not ready until initial refresh has
completed for every downstream, at least one downstream is healthy, the
aggregate is usable, and audit storage is healthy. Check downstream URLs,
credentials, MCP protocol support, and the audit path permissions.

## A tool is missing from `tools/list`

Check the downstream catalog, configured `id`, policy rules, collision errors,
and aggregate limits. Deny rules override allow rules and the default is deny.

## `tools/call` is unavailable or uncertain

`-32010` means the public tool is unavailable or denied. `-32011` is a
pre-dispatch failure. `-32012` means the downstream outcome may be unknown
after dispatch started and must not be retried automatically. `-32013` means
the terminal response was unsupported. `-32014` means audit storage was
unavailable before dispatch.

## Browser origin is forbidden

Use a configured exact loopback origin, including its port. An absent Origin is
accepted; `null`, non-loopback, and malformed origins are rejected.

## Downstream progress/SSE response

Request-scoped downstream SSE/progress is intentionally unsupported in v0.1.
Conduit rejects it conservatively and cleans up any invocation-owned session.
