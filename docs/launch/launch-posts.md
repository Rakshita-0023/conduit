# Launch posts

All compatibility language below follows the repository’s interoperability
evidence: MCP Inspector discovery and calls were verified; Codex CLI and Claude
Code transport/session connectivity were verified, while model-directed calls
require an authenticated account and were not run in the audit environment.

## A. LinkedIn

I've been working on **Conduit**, an open-source, local-first MCP gateway for connecting AI clients to multiple MCP servers through one controlled endpoint.

The problem I wanted to solve was straightforward: when a client connects independently to GitHub, Slack, database, and other MCP servers, policy, credential ownership, naming, and audit behavior get fragmented across every connection.

Conduit sits between the client and those downstream servers. It provides deterministic namespaced tools, centralized default-deny/deny-wins policy, durable audit-before-dispatch behavior, and per-server credential isolation. It also bounds downstream responses and intentionally never retries or replays a tool call automatically.

The project supports multiple Streamable HTTP downstreams and terminal SSE responses. I verified MCP Inspector discovery and tool calls, GitHub remote MCP interoperability, and standard HTTP transport/session setup with Codex CLI and Claude Code. Model-directed Codex/Claude calls still need to be rerun from authenticated accounts before I’d represent those as independently verified.

Conduit is open source, available on PyPI, and has CI, documentation, security checks, and release automation:

- GitHub: https://github.com/Rakshita-0023/conduit
- PyPI: https://pypi.org/project/conduit-gateway/
- Docs: https://rakshita-0023.github.io/conduit/
- Install: `pip install conduit-gateway`

I’d really value feedback from people building with MCP: what policy, observability, or multi-server workflows would make a gateway more useful in your setup? Contributions and issues are welcome.

## B. X / Twitter

Open-sourced Conduit: a local-first MCP gateway that puts one policy-enforced, auditable endpoint in front of multiple MCP servers. Deterministic namespaced tools, default-deny policy, credential isolation, bounded responses, no call replay. `pip install conduit-gateway` https://github.com/Rakshita-0023/conduit

## C. Hacker News / Reddit-style launch

Show HN: Conduit — a deterministic local MCP gateway for multiple downstream servers

I built Conduit because the usual “one MCP client → many independent MCP servers” topology leaves routing, credentials, policy, and audit behavior spread across each connection. Conduit instead exposes one local HTTP endpoint and federates configured Streamable HTTP downstreams behind it.

The gateway publishes namespaced public tools such as `github.search_code`, stores an exact downstream route for every published name, and applies deny-wins/default-deny policy before dispatch. It writes a durable authorization record before a downstream call can start. Configured downstream headers remain scoped to their owning server; client credentials and cookies are not forwarded. Response sizes are bounded, redirects are disabled, and a tool call is never automatically replayed after a timeout or unknown outcome.

The compatibility work was the interesting part: Conduit retains a native modern MCP profile while adapting the standard HTTP session negotiation currently used by desktop/CLI clients. MCP Inspector’s default and forced-modern modes were tested for discovery and calls. Codex CLI and Claude Code transport/session setup were tested, but model-directed calls need authenticated accounts and should not be treated as independently verified yet. GitHub’s remote MCP endpoint was also tested through Conduit, including its finite terminal-SSE response format.

Current limits are intentional: Conduit is local-first, Streamable HTTP only, does not provide an OAuth broker or stdio transport, does not bridge progress/event streams, and does not support aggregate `tools/list` pagination.

- Source: https://github.com/Rakshita-0023/conduit
- Package: https://pypi.org/project/conduit-gateway/
- Docs: https://rakshita-0023.github.io/conduit/

## D. Short developer-feedback message

Hey — I just open-sourced Conduit, a local MCP gateway that federates multiple MCP servers behind one endpoint with namespaced tools, centralized policy, credential isolation, and audit-before-dispatch. If you build with MCP, I’d appreciate a quick look at the architecture or docs and any feedback on the workflows you’d want a gateway to handle: https://github.com/Rakshita-0023/conduit
