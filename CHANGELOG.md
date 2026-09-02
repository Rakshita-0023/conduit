# Changelog

All notable changes are documented here. Releases use semantic Git tags and
[Conventional Commits](https://www.conventionalcommits.org/) to classify
release impact while Conduit remains in the `0.x` series.

## Unreleased

### Added

- A strict ingress compatibility adapter for standard session-based Streamable
  HTTP MCP clients negotiating `2025-06-18` or `2025-11-25`, while retaining
  Conduit's native `2026-07-28` profile unchanged.
- Claude Code, Codex CLI, and MCP Inspector setup documentation and
  compatibility matrix.

### Fixed

- Standard-client session GET requests now receive the required
  `text/event-stream` content type without exposing downstream SSE/progress as
  tool output.

## [0.2.0] - 2026-08-26

### Changed

- Reimplemented the active Conduit gateway in Python 3.11+ while preserving
  the v0.1.x public MCP safety contract.
- Replaced Go binary release automation with Python wheel/sdist packaging and
  PyPI trusted-publishing release automation.

### Added

- Python-native tests, linting, type checking, package validation, MkDocs
  documentation, and Python 3.11–3.14 CI.

## [0.1.1] - 2026-08-24

### Fixed

- Hardened downstream response bounds, shutdown lifecycle ownership, session
  cleanup, protected MCP routing headers, and correlated JSON-RPC error
  fidelity.

### Added

- GitHub release binary packaging for supported macOS and Linux targets and a
  Homebrew cask publication path.

## [0.1.0]

### Added

- Local-first MCP 2026-07-28 federation over Streamable HTTP.
- Deterministic namespaced discovery, policy-filtered tools, exact-route
  dispatch, and durable audit-before-dispatch behavior.

[0.1.1]: https://github.com/Rakshita-0023/conduit/releases/tag/v0.1.1
[0.1.0]: https://github.com/Rakshita-0023/conduit/releases/tag/v0.1.0

## 0.3.0

- Add compatibility ingress for standard HTTP MCP clients.
- Add Claude Code compatibility.
- Add Codex CLI compatibility.
- Support 2025-06-18 and 2025-11-25 client session negotiation.
- Preserve native modern 2026-07-28 behavior.
- Add SSE session-channel compatibility for clients such as Codex.
- Validate real Codex model-directed tool calls through multiple downstream MCP servers.
- Preserve centralized policy, audit, routing, credential isolation, and no-retry guarantees.

