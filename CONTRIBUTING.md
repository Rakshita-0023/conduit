# Contributing to Conduit

Thank you for improving Conduit. The active implementation is Python. The
released Go v0.1.x runtime is preserved by its release tags and the
`go-v0.1-maintenance` branch.

## Before opening a change

- Start from the default branch and keep a change focused.
- Use [Conventional Commits](https://www.conventionalcommits.org/): `fix:` for
  patches, `feat:` for minor-compatible additions, and `!` or a
  `BREAKING CHANGE:` footer for breaking changes.
- Do not add credentials, private downstream URLs, audit logs, or generated
  release files.
- Preserve the safety boundaries described in [docs/architecture.md](docs/architecture.md).

## Development checks

Conduit requires Python 3.11 or newer.

```sh
python -m pip install -e '.[test,docs,release]'
python -m pytest --cov
ruff check .
mypy src
python -m pip_audit
python -m build
mkdocs build --strict
git diff --check
```

Tests use local mock MCP servers only. Do not replace deterministic
synchronization with arbitrary sleeps.

## Pull requests

Explain the user-visible behavior, the safety invariant involved, and the
verification performed. Changes to dispatch, audit, registry publication,
credential isolation, bounded reads, or shutdown should include regression
tests for the relevant lifecycle.

## Reporting security issues

Please do not open a public issue for a suspected vulnerability. Follow
[SECURITY.md](SECURITY.md).
