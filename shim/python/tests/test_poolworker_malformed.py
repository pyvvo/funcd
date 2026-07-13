"""Regression tests for the shim's malformed-invoke-body guard.

A valid-JSON-but-non-object body (``null`` / ``[1]`` / ``42`` / …) used to reach ``event.get("data")``
and raise ``AttributeError`` — uncaught, crashing the pooled worker (the gateway logged
``proxy error: EOF`` and the caller got an empty-body 502). These lock in the clean 400 + worker
survival so we never regress that behavior.
"""
from __future__ import annotations

import pytest

import funcd_shim._poolworker as pw
from funcd_shim.funclog import open_channel
from funcd_shim.runtime import Validators


@pytest.fixture
def loaded_worker(monkeypatch: pytest.MonkeyPatch) -> None:
    """Inject a trivial echo handler + no validators so ``invoke`` exercises only the decode guard."""
    monkeypatch.setattr(pw, "_handler", lambda ctx, event: {"echo": event.get("data")})
    monkeypatch.setattr(pw, "_validators", Validators())  # input/output both None → no contract
    monkeypatch.setattr(pw, "_channel", open_channel())      # no-op channel (no FUNCD_LOG_FD)


def test_malformed_body_returns_400(loaded_worker: None) -> None:
    r = pw.invoke("abc{")  # not valid JSON
    assert r["status"] == 400
    assert "not valid JSON" in r["body"]["error"]


@pytest.mark.parametrize("body", ["null", "[1,2]", "42", '"s"', "true"])
def test_non_object_body_returns_400(loaded_worker: None, body: str) -> None:
    # Valid JSON, but not a CloudEvent envelope (object) → clean 400, NOT an AttributeError crash.
    r = pw.invoke(body)
    assert r["status"] == 400
    assert "CloudEvent envelope" in r["body"]["error"]


def test_contract_mismatch_422_unchanged(monkeypatch: pytest.MonkeyPatch) -> None:
    # A well-formed envelope whose data fails the input validator still returns 422 (no regression).
    monkeypatch.setattr(pw, "_handler", lambda ctx, event: {"ok": True})
    monkeypatch.setattr(pw, "_channel", open_channel())

    def reject(_data: object) -> list[str]:
        return ["data must be object"]

    monkeypatch.setattr(pw, "_validators", Validators(input=reject))
    r = pw.invoke('{"data":123}')
    assert r["status"] == 422
    assert r["body"]["error"] == "event data does not match the input contract"


def test_worker_survives_bad_input(loaded_worker: None) -> None:
    # A bad request must not poison the pooled interpreter: a subsequent good request returns 200.
    assert pw.invoke("null")["status"] == 400
    good = pw.invoke('{"data":{"x":1}}')
    assert good["status"] == 200
    assert good["body"] == {"echo": {"x": 1}}
