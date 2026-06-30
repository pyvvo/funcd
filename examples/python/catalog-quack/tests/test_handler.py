"""Unit tests for the catalog-quack consumer handler (no live catalog needed)."""

from __future__ import annotations

import handler


def test_quack_uri_adds_scheme_to_bare_host_port() -> None:
    assert handler._quack_uri("10.63.0.2:8080") == "quack://10.63.0.2:8080"


def test_quack_uri_keeps_an_explicit_quack_url() -> None:
    assert handler._quack_uri("quack://lake.default:443") == "quack://lake.default:443"
