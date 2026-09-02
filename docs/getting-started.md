# Getting started

Conduit provides one local MCP endpoint that federates configured downstream
Streamable HTTP MCP servers. It requires Python 3.11 or newer.

## Install

Install the published package with `pipx install conduit-gateway`, or build
from source:

```sh
git clone https://github.com/Rakshita-0023/conduit.git
cd conduit
python -m venv .venv
. .venv/bin/activate
python -m pip install -e .
```

## Configure and start

Create the bundled template, set each downstream URL and its policy, then start
Conduit:

```sh
conduit --init --config conduit.yaml
conduit --config conduit.yaml
```

`--init` creates a private file and refuses to overwrite one. It is available
from the published wheel as well as a source checkout.

## Run a local demo downstream

For a complete first run, install the official SDK and save this as
`demo_server.py` in a separate working directory:

```sh
python -m pip install mcp
```

```python
import asyncio

from mcp.server.mcpserver import MCPServer

server = MCPServer("demo")


@server.tool()
def hello(name: str) -> dict[str, str]:
    return {"message": f"hello, {name}"}


if __name__ == "__main__":
    asyncio.run(server.run_streamable_http_async(port=9000, json_response=True, stateless_http=True))
```

Start it in one terminal:

```sh
python demo_server.py
```

Wait until it prints the local URL before starting Conduit in another terminal.

In `conduit.yaml`, replace the generated downstream entry with:

```yaml
downstreams:
  - id: demo
    url: http://127.0.0.1:9000/mcp
    headers: {}
policy:
  allow: ["demo.*"]
  deny: []
```

Then start Conduit in a second terminal:

```sh
conduit --config conduit.yaml
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

Use a Streamable HTTP MCP client. Conduit's native `2026-07-28` profile
requires matching `MCP-Protocol-Version`, `Mcp-Method`, JSON-RPC method, and
`_meta` protocol metadata. Current standard clients using negotiated
`2025-06-18` or `2025-11-25` sessions are adapted safely at ingress; see the
[compatibility matrix](compatibility.md) for Claude Code, Codex, and Inspector
commands.

Conduit publishes tools as `<downstream-id>.<tool-name>`, such as
`github.search_code`; those public names are policy-filtered and do not need to
be parsed by a client.

With the official SDK installed, run this in a third terminal to verify the
complete path:

```python
import asyncio
from mcp.client.client import Client


async def main() -> None:
    async with Client("http://127.0.0.1:8080/mcp") as client:
        tools = await client.list_tools()
        print([tool.name for tool in tools.tools])  # ['demo.hello']
        result = await client.call_tool("demo.hello", {"name": "Conduit"})
        print(result.content[0].text)


asyncio.run(main())
```
