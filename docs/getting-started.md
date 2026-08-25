# Getting started

Conduit provides one local MCP endpoint that federates configured downstream
Streamable HTTP MCP servers. It is a Go binary today and requires the Go
version declared in `go.mod` when built from source.

## Install

Download a release archive and verify it with the published SHA-256 checksum,
install the macOS cask
with `brew install --cask Rakshita-0023/tap/conduit`, or build from source:

```sh
git clone https://github.com/Rakshita-0023/conduit.git
cd conduit
go build -o conduit ./cmd/conduit
```

## Configure and start

Copy the example, set each downstream URL and its policy, then start Conduit:

```sh
cp config.example.yaml conduit.yaml
./conduit -config conduit.yaml
```

The listener must be loopback-only. On first start Conduit refreshes every
downstream catalog. It is ready only when every downstream has attempted its
first refresh, at least one is healthy, the aggregate is usable, and audit
storage is healthy.

```sh
curl -sS http://127.0.0.1:8080/healthz
curl -sS http://127.0.0.1:8080/status
```

## Discover tools

Use a Streamable HTTP MCP client with protocol version `2026-07-28`. Requests
must include matching `MCP-Protocol-Version`, `Mcp-Method`, JSON-RPC method,
and `_meta` protocol metadata. See [README.md](../README.md#endpoints-and-mcp-calls)
for complete `curl` examples.

Conduit publishes tools as `<downstream-id>.<tool-name>`, such as
`github.search_code`; those public names are policy-filtered and do not need to
be parsed by a client.
