# Changelog

All notable changes are documented here. Releases use semantic Git tags and
[Conventional Commits](https://www.conventionalcommits.org/) to classify
release impact while Conduit remains in the `0.x` series.

## [0.2.0] - Unreleased

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
