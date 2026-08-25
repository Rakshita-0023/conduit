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
