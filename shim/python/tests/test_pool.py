"""Tests for the subinterpreter pool host (ADR-0050). Gated on Python ≥ 3.14 — they
``importorskip`` ``concurrent.interpreters`` (PEP 734), exactly as the node pool tests are
node-gated. Run with ``uv run --python 3.14 pytest``."""

from __future__ import annotations

import json
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
from pathlib import Path

import fastjsonschema
import pytest

pytest.importorskip("concurrent.interpreters")  # Python 3.14+ only

SRC = str(Path(__file__).resolve().parents[1] / "src")

# A REAL fastjsonschema-compiled __funcd_validate_input (what the build bakes) — this proves
# fastjsonschema's compiled validator runs end-to-end inside the subinterpreter pool (the whole
# reason we chose it over pydantic-core, which crashes a subinterpreter).
_SCHEMA = {
    "type": "object",
    "properties": {"hello": {"type": "string"}},
    "required": ["hello"],
    "additionalProperties": False,
}
_VALIDATOR = fastjsonschema.compile_to_code(_SCHEMA)
ECHO = (
    _VALIDATOR + "\n"
    "def __funcd_validate_input(d):\n"
    "    try:\n"
    "        validate(d)\n"
    "        return []\n"
    "    except JsonSchemaValueException as e:\n"
    "        return [str(e)]\n"
    "def handle(context, event):\n"
    "    return {'echoed': event.get('data')}\n"
)
COUNTER = (
    "_count = 0\n"
    "def handle(context, event):\n"
    "    global _count\n"
    "    _count += 1\n"
    "    return {'count': _count}\n"
)
CPU = (
    "def handle(context, event):\n"
    "    s = 0\n"
    "    for i in range(3_000_000):\n"
    "        s += i * i\n"
    "    return {'ok': True}\n"
)


def _manifest(tmp: Path, members: list[tuple[str, str]]) -> Path:
    entries = []
    for i, (name, body) in enumerate(members):
        art = tmp / f"{name}_{i}.py"
        art.write_text(body)
        entries.append({"name": name, "artifact": str(art), "handler": "handle"})
    mpath = tmp / "manifest.json"
    mpath.write_text(json.dumps(entries))
    return mpath


def _start(tmp: Path, manifest: Path) -> tuple[subprocess.Popen[bytes], int]:
    port_file = tmp / "pool.port"
    proc = subprocess.Popen(
        [sys.executable, "-m", "funcd_shim.pool"],
        env={
            "FUNCD_POOL_MANIFEST": str(manifest),
            "FUNCD_PORTFILE": str(port_file),
            "PATH": "/usr/bin:/bin",
            "PYTHONPATH": SRC,
        },
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    deadline = time.time() + 20
    while time.time() < deadline:
        if proc.poll() is not None:
            raise RuntimeError(f"pool host exited early: {proc.returncode}")
        if port_file.exists() and port_file.read_text().strip():
            return proc, int(port_file.read_text().strip())
        time.sleep(0.05)
    proc.terminate()
    raise RuntimeError("pool host never wrote its port")


def _post(port: int, name: str, body: str) -> tuple[int, bytes]:
    req = urllib.request.Request(
        f"http://127.0.0.1:{port}/function/{name}", data=body.encode(),
        headers={"content-type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=10) as r:  # noqa: S310 - loopback
            return r.status, r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def test_pool_colocates_and_contract(tmp_path: Path) -> None:
    # scenario: py-pool-colocates + py-pool-contract — two handlers in one host; same wire contract.
    proc, port = _start(tmp_path, _manifest(tmp_path, [("f0", ECHO), ("f1", ECHO)]))
    try:
        for name in ("f0", "f1"):
            st, body = _post(port, name, json.dumps({"data": {"hello": "world"}}))
            assert st == 200, body
            assert json.loads(body) == {"echoed": {"hello": "world"}}
        # input-contract mismatch → 422 (the per-handler pydantic validator, ADR-0058)
        st, body = _post(port, "f0", json.dumps({"data": {"hello": 5}}))
        assert st == 422
        assert json.loads(body)["error"] == "event data does not match the input contract"
        # health + unknown route
        with urllib.request.urlopen(f"http://127.0.0.1:{port}/health/readiness", timeout=5) as r:  # noqa: S310
            assert r.status == 200
        assert _post(port, "nope", "{}")[0] == 404
    finally:
        proc.terminate()
        proc.wait(timeout=5)


def test_pool_keep_alive_no_desync(tmp_path: Path) -> None:
    # HTTP/1.1 keep-alive on the pool host: requests on ONE connection, incl. a 404 POST with a body
    # (an unknown /function/<name>), must not desync — the body is drained before the early return.
    import http.client

    proc, port = _start(tmp_path, _manifest(tmp_path, [("f0", ECHO)]))
    try:
        conn = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
        conn.request("POST", "/function/f0", b'{"data":{"hello":"a"}}')
        r = conn.getresponse()
        assert r.status == 200
        assert json.loads(r.read())["echoed"] == {"hello": "a"}

        conn.request("POST", "/function/nope", b'{"junk":true}')  # 404 WITH a body
        r = conn.getresponse()
        assert r.status == 404
        r.read()

        conn.request("POST", "/function/f0", b'{"data":{"hello":"b"}}')  # same conn still aligned
        r = conn.getresponse()
        assert r.status == 200
        assert json.loads(r.read())["echoed"] == {"hello": "b"}
        conn.close()
    finally:
        proc.terminate()
        proc.wait(timeout=5)


def test_pool_isolates(tmp_path: Path) -> None:
    # scenario: py-pool-isolates — same artifact deployed twice; each handler has its OWN interpreter
    # state, so their counters are independent (a shared interpreter would share the module global).
    proc, port = _start(tmp_path, _manifest(tmp_path, [("f0", COUNTER), ("f1", COUNTER)]))
    try:
        for _ in range(3):
            _post(port, "f0", "{}")
        _, last_f0 = _post(port, "f0", "{}")
        _, first_f1 = _post(port, "f1", "{}")
        assert json.loads(last_f0)["count"] == 4
        assert json.loads(first_f1)["count"] == 1  # NOT 5 — separate interpreter state
    finally:
        proc.terminate()
        proc.wait(timeout=5)


def test_pool_parallel(tmp_path: Path) -> None:
    # scenario: py-pool-parallel — CPU-bound handlers run on per-interpreter GILs, so two concurrent
    # requests to different handlers finish in well under 2× a single's time (a shared GIL would
    # serialize them ≈2×). Lenient bound to tolerate scheduling noise.
    proc, port = _start(tmp_path, _manifest(tmp_path, [("f0", CPU), ("f1", CPU)]))
    try:
        t0 = time.perf_counter()
        _post(port, "f0", "{}")
        single = time.perf_counter() - t0

        results: list[float] = []
        lock = threading.Lock()

        def hit(name: str) -> None:
            s = time.perf_counter()
            _post(port, name, "{}")
            with lock:
                results.append(time.perf_counter() - s)

        t0 = time.perf_counter()
        ts = [threading.Thread(target=hit, args=(n,)) for n in ("f0", "f1")]
        for t in ts:
            t.start()
        for t in ts:
            t.join()
        concurrent = time.perf_counter() - t0
        assert concurrent < 1.7 * single, (
            f"two concurrent CPU handlers serialized: {concurrent:.3f}s vs single {single:.3f}s"
        )
    finally:
        proc.terminate()
        proc.wait(timeout=5)


def test_pool_delivered_contract_enforces(tmp_path: Path) -> None:
    # scenario: runtime-compiles-validator (pool) — a member whose manifest carries a "contract"
    # path gets its validator COMPILED from that delivered schema at init (no baked symbol), and
    # enforces input→422 exactly like the solo shim.
    art = tmp_path / "schema_only.py"
    # a schema-only handler: NO __funcd_validate_* baked in — validation comes from the contract.
    art.write_text("def handle(context, event):\n    return {'echoed': event.get('data')}\n")
    cpath = tmp_path / "contract.json"
    cpath.write_text(json.dumps({
        "input": {"type": "object", "properties": {"hello": {"type": "string"}},
                  "required": ["hello"], "additionalProperties": False},
        "output": {},
    }))
    mpath = tmp_path / "m.json"
    mpath.write_text(json.dumps([
        {"name": "f0", "artifact": str(art), "handler": "handle", "contract": str(cpath)},
    ]))

    proc, port = _start(tmp_path, mpath)
    try:
        assert _post(port, "f0", json.dumps({"data": {"hello": "world"}}))[0] == 200
        st, body = _post(port, "f0", json.dumps({"data": {"hello": 5}}))
        assert st == 422, body
    finally:
        proc.terminate()
        proc.wait(timeout=5)


def test_pool_broken_contract_fails_closed(tmp_path: Path) -> None:
    # scenario: no-fail-open (pool) — a member with a set-but-missing contract path fails the whole
    # host closed (exit 3), never serving un-validated.
    art = tmp_path / "fn.py"
    art.write_text("def handle(context, event):\n    return None\n")
    mpath = tmp_path / "m.json"
    mpath.write_text(json.dumps([
        {"name": "f0", "artifact": str(art), "handler": "handle", "contract": str(tmp_path / "absent.json")},
    ]))
    proc = subprocess.Popen(
        [sys.executable, "-m", "funcd_shim.pool"],
        env={"FUNCD_POOL_MANIFEST": str(mpath), "FUNCD_PORTFILE": str(tmp_path / "p"),
             "PATH": "/usr/bin:/bin", "PYTHONPATH": SRC},
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    assert proc.wait(timeout=15) == 3


def test_pool_shape_gate(tmp_path: Path) -> None:
    # scenario: a member whose handler export is missing makes the whole host exit 3.
    bad = tmp_path / "bad.py"
    bad.write_text("x = 1\n")  # no handle
    entries = [{"name": "f0", "artifact": str(bad), "handler": "handle"}]
    mpath = tmp_path / "m.json"
    mpath.write_text(json.dumps(entries))
    proc = subprocess.Popen(
        [sys.executable, "-m", "funcd_shim.pool"],
        env={"FUNCD_POOL_MANIFEST": str(mpath), "FUNCD_PORTFILE": str(tmp_path / "p"),
             "PATH": "/usr/bin:/bin", "PYTHONPATH": SRC},
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    assert proc.wait(timeout=15) == 3
