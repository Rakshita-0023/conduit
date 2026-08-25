"""Command-line entry point for the local Conduit gateway."""

from __future__ import annotations

import argparse
from collections.abc import Sequence

import uvicorn

from . import __version__
from .app import create_app
from .config import ConfigError, load_config


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="conduit")
    parser.add_argument("--config", "-config", default="conduit.yaml", help="path to the Conduit YAML configuration")
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    try:
        config = load_config(args.config)
    except ConfigError as exc:
        _parser().error(str(exc))
    host, port = _listener_host_port(config.listener.address)
    uvicorn.run(
        create_app(config, build_version=__version__),
        host=host,
        port=port,
        workers=1,
        log_level="info",
    )
    return 0


def _listener_host_port(address: str) -> tuple[str, int]:
    if address.startswith("["):
        end = address.index("]")
        return address[1:end], int(address[end + 2 :])
    host, port = address.rsplit(":", 1)
    return host, int(port)


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
