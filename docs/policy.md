# Policy

Rules match a public namespaced tool exactly or with a trailing `.*`. Evaluation
is deny first, then allow; tools not allowed are hidden and unavailable.

Routes are stored at catalog publication time. Conduit never reconstructs a
route by splitting a public name.

## Public tool-name grammar

Conduit publishes exactly `<downstream-id>.<downstream-tool-name>`. Both
components are ASCII letters, digits, hyphens, or underscores: downstream IDs
are 1–64 characters and downstream tool names are 1–128 characters. The dot
is Conduit's namespace separator and is not permitted inside either component.
Names and policy rules are case-sensitive.

Valid policy rules are either an exact public name such as `github.search`, or
one namespace wildcard such as `github.*`. Empty values, bare/broad wildcards,
extra dots, whitespace, control characters, Unicode lookalikes,
percent-encoded separators, and other non-grammar patterns are rejected at
configuration or downstream-catalog validation time. A malformed downstream
catalog degrades that downstream; it is never made routable.
