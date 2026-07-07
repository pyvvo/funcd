"""Tests for the log-burst function — the author's own unit tests, no shim/platform needed.

These assert the handler emits >= 100 records and reports the count it emitted. The actual *capture*
(the Path B logging.Handler → side channel → blob) is the platform's job, exercised by the shim's
test_funclog.py and the Lima e2e; here we just prove the function emits the burst and counts it.
"""

import logging

from funcd_shim import CloudEvent

from handler import FuncInput, handle


class _Ctx:
    def log(self, *args: object) -> None:
        pass


def test_handle_emits_at_least_100_records() -> None:
    event: CloudEvent[FuncInput] = {"id": "1", "source": "s", "type": "t", "data": {}}
    result = handle(_Ctx(), event)
    assert result["emitted"] >= 100


def test_emitted_count_matches_records_seen_by_a_handler() -> None:
    # Attach a counting handler to the root logger (the shim's Path B seam) and confirm the function's
    # reported `emitted` equals the number of records that actually flowed through logging.
    records: list[logging.LogRecord] = []

    class _Collect(logging.Handler):
        def emit(self, record: logging.LogRecord) -> None:
            records.append(record)

    root = logging.getLogger()
    saved_level = root.level
    collector = _Collect()
    root.addHandler(collector)
    root.setLevel(logging.INFO)
    try:
        event: CloudEvent[FuncInput] = {"id": "1", "source": "s", "type": "t", "data": {"count": 25}}
        result = handle(_Ctx(), event)
    finally:
        root.removeHandler(collector)
        root.setLevel(saved_level)

    assert result["emitted"] == len(records)
    assert result["emitted"] == 125  # 90 + 25 INFO, 7 WARN, 3 ERROR
    sevs = {r.levelname for r in records}
    assert {"INFO", "WARNING", "ERROR"} <= sevs
