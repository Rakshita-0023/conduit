"""Command-line entry point for the local Conduit gateway."""

from __future__ import annotations

import argparse
import os
from collections.abc import Sequence
from importlib import resources
from pathlib import Path

import uvicorn

from . import __version__
from .app import create_app
from .config import ConfigError, load_config


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="conduit")
    parser.add_argument("--config", "-config", default="conduit.yaml", help="path to the Conduit YAML configuration")
    parser.add_argument("--init", action="store_true", help="write the bundled example configuration and exit")
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    if args.init:
        try:
            _write_example(args.config)
        except OSError as exc:
            _parser().error(f"write configuration: {exc}")
        print(f"created {args.config}")
        return 0
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


def _write_example(path: str) -> None:
    """Create a private config from the package-owned template without overwrite."""

    target = Path(path)
    template = resources.files("conduit").joinpath("conduit.example.yaml").read_text(encoding="utf-8")
    descriptor = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as output:
        output.write(template)


def _listener_host_port(address: str) -> tuple[str, int]:
    if address.startswith("["):
        end = address.index("]")
        return address[1:end], int(address[end + 2 :])
    host, port = address.rsplit(":", 1)
    return host, int(port)


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
