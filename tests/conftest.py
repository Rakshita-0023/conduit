from __future__ import annotations

import textwrap

import pytest

from conduit.config import Config, parse_config


@pytest.fixture
def config_text() -> str:
    return textwrap.dedent(
        """\
        listener:
          address: 127.0.0.1:8080
          allowed_origins: []
        audit:
          path: ./conduit-audit.jsonl
        policy:
          allow: [example.*]
          deny: []
        limits:
          max_pages_per_downstream: 32
          max_tools_per_downstream: 256
          max_downstream_catalog_bytes: 1048576
          max_aggregate_tools: 512
          max_aggregate_response_bytes: 4194304
          max_tool_response_bytes: 4194304
          catalog_refresh_interval: 60s
          request_timeout: 10s
          tool_call_timeout: 30s
        downstreams:
          - id: example
            url: http://127.0.0.1:9000/mcp
            headers:
              X-Downstream-Key: test-only
        """
    )


@pytest.fixture
def config(config_text: str) -> Config:
    import yaml

    return parse_config(yaml.safe_load(config_text))
