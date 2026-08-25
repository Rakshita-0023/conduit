"""Small deterministic allow/deny policy evaluation."""

from __future__ import annotations

import hashlib

from .config import PolicyConfig


class Policy:
    """Immutable deny-wins policy compiled from validated configuration."""

    def __init__(self, config: PolicyConfig) -> None:
        self._allow = tuple(config.allow)
        self._deny = tuple(config.deny)

    def allowed(self, name: str) -> bool:
        return not any(_matches(rule, name) for rule in self._deny) and any(_matches(rule, name) for rule in self._allow)

    @property
    def digest(self) -> str:
        payload = b"".join(b"\0" + rule.encode() for rule in sorted(self._allow)) + b"\1" + b"".join(
            b"\0" + rule.encode() for rule in sorted(self._deny)
        ) + b"\1"
        return "sha256:" + hashlib.sha256(payload).hexdigest()


def _matches(rule: str, name: str) -> bool:
    return rule == name or (rule.endswith(".*") and name.startswith(rule[:-1]))
