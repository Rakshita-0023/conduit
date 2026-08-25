# Development

Install the Go toolchain declared in `go.mod`, then run the complete local
verification set:

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

The [Phase 0 parity oracle](python-migration-parity.md) executes local mock
downstreams against the real public Go HTTP boundary. Run it directly with:

```sh
go test ./internal/parity -count=1 -timeout 60s
```

Do not broaden the MCP protocol profile while fixing a focused behavior.
Changes involving dispatch, shutdown, audit ordering, or bounded reads need
deterministic regression coverage.
