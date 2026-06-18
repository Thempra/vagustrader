import logging
from polywin.logger import get_logger, setup_logging


def test_get_logger_returns_named_logger():
    logger = get_logger("polywin.test")
    assert isinstance(logger, logging.Logger)
    assert logger.name == "polywin.test"


def test_get_logger_same_instance():
    assert get_logger("polywin.x") is get_logger("polywin.x")


def test_setup_logging_creates_log_dir(tmp_path):
    log_dir = str(tmp_path / "logs")
    setup_logging(log_dir=log_dir)
    import os
    assert os.path.isdir(log_dir)
