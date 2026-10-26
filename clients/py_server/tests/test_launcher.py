import pytest

import gligabao_server


def test_binary_path_missing(monkeypatch, tmp_path):
    monkeypatch.setattr(gligabao_server, "_BIN_DIR", tmp_path)
    with pytest.raises(FileNotFoundError):
        gligabao_server.binary_path()


def test_binary_path_and_config_path(monkeypatch, tmp_path):
    monkeypatch.setattr(gligabao_server, "_BIN_DIR", tmp_path)
    monkeypatch.setattr(gligabao_server.sys, "platform", "linux")
    (tmp_path / "gligabao").write_text("")
    assert gligabao_server.binary_path() == str(tmp_path / "gligabao")
    assert gligabao_server.config_path() == str(tmp_path / "gligabao.toml")


def test_main_runs_the_binary_with_the_arguments(monkeypatch, tmp_path):
    monkeypatch.setattr(gligabao_server, "_BIN_DIR", tmp_path)
    monkeypatch.setattr(gligabao_server.sys, "platform", "linux")
    monkeypatch.setattr(gligabao_server.sys, "argv", ["gligabao-server", "serve", "--db", "x.db"])
    (tmp_path / "gligabao").write_text("")
    called = {}
    monkeypatch.setattr(gligabao_server.os, "execv", lambda path, args: called.update(path=path, args=args))
    gligabao_server.main()
    assert called["args"] == [str(tmp_path / "gligabao"), "serve", "--db", "x.db"]


def test_main_without_a_binary_exits(monkeypatch, tmp_path):
    monkeypatch.setattr(gligabao_server, "_BIN_DIR", tmp_path)
    with pytest.raises(SystemExit) as e:
        gligabao_server.main()
    assert "gligabao binary" in str(e.value)
