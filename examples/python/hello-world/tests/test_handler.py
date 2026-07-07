"""Tests for the Python hello-world function — the author's own unit tests, no shim needed.

The platform owns input/output *validation* (the push build generates the schema from
``FuncInput``/``FuncOutput`` and bakes a fastjsonschema validator; the shim runs it), so these
tests exercise the handler directly on already-valid input.
"""

from funcd_shim import CloudEvent

from handler import FuncInput, handle


class _Ctx:
    def log(self, *args: object) -> None:
        pass


def test_handle_greets() -> None:
    event: CloudEvent[FuncInput] = {"id": "1", "source": "s", "type": "t", "data": {"name": "world"}}
    result = handle(_Ctx(), event)
    assert result == {"greeting": "Hello, world."}


def test_funcinput_is_a_plain_dict_at_runtime() -> None:
    # A TypedDict is runtime-honest: an instance is just a dict, exactly what the shim delivers.
    data: FuncInput = {"name": "world"}
    assert isinstance(data, dict)
