from __future__ import annotations

from conduit import cli


def test_cli_runs_one_uvicorn_worker(monkeypatch, tmp_path, config_text: str) -> None:
    path = tmp_path / "conduit.yaml"
    path.write_text(config_text)
    observed = {}

    def fake_run(app, **kwargs) -> None:
        observed["app"] = app
        observed.update(kwargs)

    monkeypatch.setattr(cli.uvicorn, "run", fake_run)

    assert cli.main(["--config", str(path)]) == 0
    assert observed["host"] == "127.0.0.1"
    assert observed["port"] == 8080
    assert observed["workers"] == 1


def test_cli_init_writes_private_template_without_overwriting(tmp_path) -> None:
    path = tmp_path / "conduit.yaml"

    assert cli.main(["--init", "--config", str(path)]) == 0
    assert "downstreams:" in path.read_text(encoding="utf-8")
    assert path.stat().st_mode & 0o077 == 0
    try:
        cli.main(["--init", "--config", str(path)])
    except SystemExit as exc:
        assert exc.code == 2
    else:  # pragma: no cover - defensive assertion
        raise AssertionError("init unexpectedly overwrote an existing configuration")
