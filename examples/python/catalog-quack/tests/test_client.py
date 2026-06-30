"""Unit tests for the catalog-quack client (no live server needed)."""

from __future__ import annotations

import catalog_quack_client as client


def test_quack_uri_adds_scheme_to_bare_host_port() -> None:
    assert client.quack_uri("10.63.0.2:8080") == "quack://10.63.0.2:8080"


def test_quack_uri_keeps_an_explicit_quack_url() -> None:
    assert client.quack_uri("quack://lake.default:443") == "quack://lake.default:443"


def test_main_requires_endpoint_and_token() -> None:
    # Missing --endpoint/--token (and no env) ⇒ exit 2, a usage error (no live server contacted).
    assert client.main(["--sql", "SELECT 1"]) == 2
