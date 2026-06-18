import json
import logging
import sys
from datetime import datetime, timezone
from logging.handlers import RotatingFileHandler
from pathlib import Path


class _JsonFormatter(logging.Formatter):
    def format(self, record: logging.LogRecord) -> str:
        data: dict = {
            "ts": datetime.fromtimestamp(record.created, tz=timezone.utc).isoformat(),
            "level": record.levelname,
            "logger": record.name,
            "msg": record.getMessage(),
        }
        if record.exc_info:
            data["exc"] = self.formatException(record.exc_info)
        return json.dumps(data)


def setup_logging(log_dir: str = "logs") -> None:
    Path(log_dir).mkdir(parents=True, exist_ok=True)
    fmt = _JsonFormatter()
    root = logging.getLogger()
    root.setLevel(logging.INFO)
    if not root.handlers:
        sh = logging.StreamHandler(sys.stdout)
        sh.setFormatter(fmt)
        root.addHandler(sh)
        fh = RotatingFileHandler(
            f"{log_dir}/polywin.log", maxBytes=10 * 1024 * 1024, backupCount=5
        )
        fh.setFormatter(fmt)
        root.addHandler(fh)


def get_logger(name: str) -> logging.Logger:
    return logging.getLogger(name)
