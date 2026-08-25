## Summary

Describe the user-visible behavior and the problem this change solves.

## Safety and compatibility

- [ ] This preserves the MCP 2026-07-28 / Streamable HTTP compatibility contract.
- [ ] This does not introduce automatic `tools/call` retries or forwarding of caller credentials.
- [ ] I considered policy, exact-route, audit-before-dispatch, bounds, and shutdown invariants where relevant.
- [ ] I updated documentation and tests where behavior changed.

## Verification

List the commands and focused tests run. Do not include credentials, private
downstream URLs, or audit-log data.
