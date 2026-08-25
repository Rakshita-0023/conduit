"""Conduit: a deterministic, local-first MCP federation gateway."""

__version__ = "0.2.0"

from .app import create_app
from .config import Config, ConfigError, load_config

__all__ = ["Config", "ConfigError", "create_app", "load_config"]
