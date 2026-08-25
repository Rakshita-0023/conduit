# Development

Install Python 3.11 or newer, then run the complete local verification set:

```sh
python -m pip install -e '.[test,docs]'
python -m pytest --cov
ruff check .
mypy src
python -m build
python -m pip_audit
mkdocs build --strict
git diff --check
```

Do not broaden the MCP protocol profile while fixing a focused behavior.
Changes involving dispatch, shutdown, audit ordering, or bounded reads need
deterministic regression coverage.
