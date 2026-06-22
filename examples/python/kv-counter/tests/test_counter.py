"""Tests for the Python kv-counter function — the author's own unit tests, no platform needed.

The platform owns input/output *validation* (the push build bakes a fastjsonschema validator the shim
runs), so these exercise the handler directly on already-valid input, with a fake ``context.kv`` backed
by an in-memory dict — proving the read-increment-write logic without a running KV service.
"""

from typing import Any

from funcd_shim import CloudEvent
from funcd_shim.kv import KVClient

from counter import FuncInput, handle


class _KV(KVClient):
    """An in-memory stand-in for the real KVClient — binding+key → bytes (no socket)."""

    def __init__(self) -> None:
        self.store: dict[tuple[str, str], bytes] = {}

    def get(self, binding: str, key: str) -> bytes | None:
        return self.store.get((binding, key))

    def put(self, binding: str, key: str, value: bytes | str) -> None:
        self.store[(binding, key)] = value.encode() if isinstance(value, str) else value


class _Ctx:
    def __init__(self) -> None:
        self.kv: KVClient = _KV()

    def log(self, *args: object) -> None:
        pass

    def invoke(self, alias: str, payload: Any) -> Any:
        raise NotImplementedError


def _event(name: str) -> CloudEvent[FuncInput]:
    return {"id": "1", "source": "s", "type": "t", "data": {"name": name}}


def test_counter_increments_per_name() -> None:
    ctx = _Ctx()
    assert handle(ctx, _event("alice")) == {"name": "alice", "count": 1}
    assert handle(ctx, _event("alice")) == {"name": "alice", "count": 2}
    # a different name counts independently
    assert handle(ctx, _event("bob")) == {"name": "bob", "count": 1}


def test_counter_persists_in_the_kv_binding() -> None:
    ctx = _Ctx()
    handle(ctx, _event("alice"))
    assert isinstance(ctx.kv, _KV)
    assert ctx.kv.store[("py-counters", "alice")] == b"1"
