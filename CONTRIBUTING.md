# Contributing to Conduit

Thank you for improving Conduit. The released Go implementation is the
current production runtime; work in `python/` is an explicitly temporary
migration workspace and must not change the Go v0.1.x behavior accidentally.

## Before opening a change

- Start from the default branch and keep a change focused.
- Use [Conventional Commits](https://www.conventionalcommits.org/): `fix:` for
  patches, `feat:` for minor-compatible additions, and `!` or a
  `BREAKING CHANGE:` footer for breaking changes.
- Do not add credentials, private downstream URLs, audit logs, or generated
  release files.
- Preserve the safety boundaries described in [docs/architecture.md](docs/architecture.md).

## Development checks

Conduit requires the Go version declared in `go.mod`.

```sh
go mod tidy
go mod verify
gofmt -w $(rg --files -g '*.go')
go vet ./...
go test ./... -count=1 -timeout 60s
go test -race ./... -count=1 -timeout 90s
go build ./...
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
