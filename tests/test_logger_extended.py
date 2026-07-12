# tests/test_logger_extended.py
"""Extended logger tests to cover _JsonFormatter and setup_logging handler branch."""
from __future__ import annotations
import json
import logging

from polywin.logger import _JsonFormatter, setup_logging


def test_json_formatter_basic():
    fmt = _JsonFormatter()
    record = logging.LogRecord(
        name="polywin.test", level=logging.INFO,
        pathname="", lineno=0, msg="hello world", args=(), exc_info=None,
    )
    output = fmt.format(record)
    data = json.loads(output)
    assert data["level"] == "INFO"
    assert data["logger"] == "polywin.test"
    assert data["msg"] == "hello world"
    assert "ts" in data


def test_json_formatter_with_exception():
    fmt = _JsonFormatter()
    try:
        raise ValueError("boom")
    except ValueError:
        import sys
        exc_info = sys.exc_info()

    record = logging.LogRecord(
        name="polywin.test", level=logging.ERROR,
        pathname="", lineno=0, msg="oops", args=(), exc_info=exc_info,
    )
    output = fmt.format(record)
    data = json.loads(output)
    assert "exc" in data
    assert "ValueError" in data["exc"]


def test_setup_logging_idempotent(tmp_path):
    """Calling setup_logging twice should not add extra handlers."""
    # Use a fresh child logger namespace so existing root handlers don't interfere
    log_dir = str(tmp_path / "logs2")

    # Remove all existing root handlers to ensure clean state
    root = logging.getLogger()
    original_handlers = root.handlers[:]
    root.handlers.clear()

    try:
        setup_logging(log_dir=log_dir)
        count_after_first = len(root.handlers)
        # Second call — handlers already present, so branch is skipped
        setup_logging(log_dir=log_dir)
        count_after_second = len(root.handlers)
        assert count_after_second == count_after_first
    finally:
        # Restore original handlers
        root.handlers[:] = original_handlers
