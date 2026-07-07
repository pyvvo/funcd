"""Scenario tests for the context.kv typed read accessors (ADR-0070): get_str/get_json over a minimal fake
worker-node local API (HTTP-over-UDS), proving the client-side decoders without a real platform."""

import os
import shutil
import tempfile
import threading
from collections.abc import Iterator
from http.server import BaseHTTPRequestHandler
from socketserver import UnixStreamServer

import pytest

from funcd_shim.kv import KVClient

# path -> (status, body); set per test, read by the handler.
_ROUTES: dict[str, tuple[int, bytes]] = {}


class _Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.0"  # close per response → the client reads to EOF (no content-length dance)

    def do_GET(self) -> None:
        route = _ROUTES.get(self.path)
        if route is None:
            self.send_response(404)
            self.end_headers()
            self.wfile.write(b"not found")
            return
        status, body = route
        self.send_response(status)
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_args: object) -> None:  # silence the test server
        pass


@pytest.fixture
def kv() -> Iterator[KVClient]:
    _ROUTES.clear()
    # A short dir under /tmp — pytest's tmp_path overflows the ~104-char AF_UNIX limit.
    tmp = tempfile.mkdtemp(dir="/tmp")
    sock = os.path.join(tmp, "s")
    server = UnixStreamServer(sock, _Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    os.environ["FUNCD_INVOKE_SOCKET"] = sock
    try:
        yield KVClient()
    finally:
        os.environ.pop("FUNCD_INVOKE_SOCKET", None)
        server.shutdown()
        server.server_close()
        shutil.rmtree(tmp, ignore_errors=True)


def test_scenario_get_str_decodes(kv: KVClient) -> None:
    _ROUTES["/kv/counters/n"] = (200, b"5")
    assert kv.get_str("counters", "n") == "5"


def test_scenario_get_json_parses(kv: KVClient) -> None:
    _ROUTES["/kv/counters/n"] = (200, b'{"n":5}')
    assert kv.get_json("counters", "n") == {"n": 5}


def test_scenario_get_typed_missing_is_none(kv: KVClient) -> None:
    assert kv.get_str("counters", "x") is None
    assert kv.get_json("counters", "x") is None


def test_scenario_get_bytes_unchanged(kv: KVClient) -> None:
    _ROUTES["/kv/counters/n"] = (200, b"5")
    assert kv.get("counters", "n") == b"5"
