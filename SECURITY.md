# Security policy

## Reporting a vulnerability

Use GitHub's private vulnerability reporting for this repository when it is
available. Do not include exploitable details, credentials, private endpoints,
or audit data in a public issue.

If private reporting is unavailable, contact the repository maintainer through
the private contact method listed on their GitHub profile. Do not use a public
issue as a substitute.

## Scope

Reports are especially useful for issues affecting:

- local listener or origin restrictions;
- MCP ingress validation and request-ID handling;
- policy enforcement and exact route dispatch;
- audit-before-dispatch ordering;
- credential/header isolation;
- bounded catalog or tool-response reads;
- session cleanup and shutdown ordering; or
- release workflow and dependency integrity.

Please include a minimal reproduction, affected version/tag, impact, and any
mitigation you identified. Allow maintainers reasonable time to investigate
and publish a fix before disclosure.

## Operational guidance

Conduit is local-first. Keep its listener loopback-only, protect audit files,
and configure downstream credentials only in the downstream entry that owns
them. Conduit is not an identity, OAuth-brokerage, or approval system.
