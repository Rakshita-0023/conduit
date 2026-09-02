#!/usr/bin/env python3
"""Safe deterministic MCP downstreams used by the Conduit launch demo.

The servers implement only the small modern JSON-RPC surface Conduit needs for
catalog discovery and tools/call. They never use credentials or external I/O.
"""

from __future__ import annotations

import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

PROTOCOL_VERSION = "2026-07-28"


class DemoServer:
    def __init__(self, port: int, tools: list[dict[str, Any]], calls: dict[str, dict[str, Any]]) -> None:
        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_: object) -> None:
                return

            def do_POST(self) -> None:  # noqa: N802
                content_length = int(self.headers.get("Content-Length", "0"))
                request = json.loads(self.rfile.read(content_length))
                method = request["method"]
                if method == "server/discover":
                    result: dict[str, Any] = {"supportedVersions": [PROTOCOL_VERSION]}
                elif method == "tools/list":
                    result = {"tools": tools}
                elif method == "tools/call":
                    name = request["params"]["name"]
                    if name == "add":
                        arguments = request["params"].get("arguments", {})
                        result = {"content": [{"type": "text", "text": str(arguments["a"] + arguments["b"])}]}
                    else:
                        result = calls[name]
                else:
                    self._write({"jsonrpc": "2.0", "id": request.get("id"), "error": {"code": -32601, "message": "method not found"}})
                    return
                self._write({"jsonrpc": "2.0", "id": request["id"], "result": result})

            def _write(self, response: dict[str, Any]) -> None:
                encoded = json.dumps(response, separators=(",", ":")).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(encoded)))
                self.end_headers()
                self.wfile.write(encoded)

        self.server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)

    def start(self) -> None:
        self.thread.start()

    def close(self) -> None:
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)


def main() -> None:
    calc = DemoServer(
        9000,
        [
            {"name": "add", "description": "Add two integers", "inputSchema": {"type": "object", "properties": {"a": {"type": "integer"}, "b": {"type": "integer"}}, "required": ["a", "b"]}},
            {"name": "multiply", "description": "Multiply two integers", "inputSchema": {"type": "object"}},
        ],
        {"multiply": {"content": [{"type": "text", "text": "demo multiply"}]}},
    )
    admin = DemoServer(
        9001,
        [{"name": "reset", "description": "A deliberately denied demo tool", "inputSchema": {"type": "object"}}],
        {"reset": {"content": [{"type": "text", "text": "should never run"}]}},
    )
    calc.start()
    admin.start()
    print("safe demo downstreams ready on :9000 and :9001", flush=True)
    try:
        threading.Event().wait()
    except KeyboardInterrupt:
        pass
    finally:
        calc.close()
        admin.close()


if __name__ == "__main__":
    main()
