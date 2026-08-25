# Conduit Python migration — Phase 1

This directory contains development-only migration work toward a future Python
implementation of Conduit. It is **not** a production replacement for the
released Go gateway and is not published as a Python package.

Phase 1 provides strict configuration parsing, a local Uvicorn process,
`/healthz`, `/status`, and the narrow MCP HTTP ingress profile needed for
`server/discover`. Because downstream discovery has not started, the process
truthfully remains not ready and valid `server/discover` requests return the
same `503` unavailable response that Go returns before its initial catalog is
ready. The status schema is retained, with the unimplemented audit reported as
`audit_healthy: false`; federation, tools, policy, durable audit, and
downstream dispatch begin in later phases.

The Go v0.1.x implementation and [`../docs/python-migration-parity.md`](../docs/python-migration-parity.md)
remain the behavioral oracle.

## Local development

Python 3.11 or newer is required.

```sh
cd python
python -m venv .venv
. .venv/bin/activate
python -m pip install -e '.[test]'
python -m pytest
```

Start the Phase 1 process with a Conduit-compatible configuration file:

```sh
conduit --config ../conduit.yaml
```

Only the configured loopback listener is bound. Stop it with `SIGINT` or
`SIGTERM`; the Starlette lifespan marks the process no longer live before the
server exits.
