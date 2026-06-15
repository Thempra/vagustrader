FROM python:3.11-slim

WORKDIR /app

COPY pyproject.toml .
COPY src/ src/

RUN pip install --no-cache-dir -e .
RUN mkdir -p data logs

CMD ["polywin", "run"]
