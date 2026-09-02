#!/usr/bin/env python3
"""Minimal standard-library client for the deterministic Conduit demo."""

from __future__ import annotations

import json
import sys
from typing import Any
from urllib.request import Request, urlopen

URL = "http://127.0.0.1:8080/mcp"
PROTOCOL_VERSION = "2025-11-25"


def post(payload: dict[str, Any], session: str | None = None) -> tuple[dict[str, Any], dict[str, str]]:
    headers = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
    if session:
        headers["MCP-Protocol-Version"] = PROTOCOL_VERSION
        headers["Mcp-Session-Id"] = session
    request = Request(URL, data=json.dumps(payload).encode(), headers=headers, method="POST")
    with urlopen(request, timeout=3) as response:  # noqa: S310 - fixed loopback URL
        return json.loads(response.read()), dict(response.headers.items())


def session() -> str:
    reply, headers = post(
        {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": PROTOCOL_VERSION, "capabilities": {}, "clientInfo": {"name": "conduit-demo", "version": "1"}}}
    )
    if "error" in reply:
        raise RuntimeError(reply["error"])
    for name, value in headers.items():
        if name.lower() == "mcp-session-id":
            return value
    raise RuntimeError("Conduit did not return an MCP session ID")


def main() -> None:
    if len(sys.argv) < 2 or sys.argv[1] not in {"list", "call"}:
        raise SystemExit("usage: client.py list | client.py call <tool> <json-arguments>")
    active_session = session()
    if sys.argv[1] == "list":
        reply, _ = post({"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}}, active_session)
        print("published tools:", ", ".join(tool["name"] for tool in reply["result"]["tools"]))
        return
    if len(sys.argv) != 4:
        raise SystemExit("usage: client.py call <tool> <json-arguments>")
    reply, _ = post(
        {"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": sys.argv[2], "arguments": json.loads(sys.argv[3])}},
        active_session,
    )
    if "error" in reply:
        error = reply["error"]
        print(f"blocked: {error['code']} {error['message']}")
        return
    print("result:", reply["result"]["content"][0]["text"])


if __name__ == "__main__":
    main()
