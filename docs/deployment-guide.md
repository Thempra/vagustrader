# Deployment Guide

## Architecture

```
┌─────────────┐     ┌─────────────┐
│   Bot       │     │  Dashboard  │
│  Container  │     │  Container  │
│             │     │             │
│  /app/data  │◄────│  /app/data  │  ← shared SQLite volume
│  /app/logs  │     │             │
│  /app/config│     │  :8080      │  ← mapped to :18080
└──────┬──────┘     └─────────────┘
       │
       │  outbound HTTPS
       ▼
┌─────────────┐
│ Polymarket  │
│ APIs        │
│ (CLOB,      │
│  Gamma,     │
│  Data API)  │
└─────────────┘
```

---

## Docker Deployment

### docker-compose.yml

```yaml
services:
  bot:
    build: .
    command: vagustrader run --paper
    env_file: .env
    volumes:
      - ./config.yml:/app/config.yml:ro
      - ./data:/app/data
      - ./logs:/app/logs
    restart: unless-stopped

  dashboard:
    build: .
    command: vagustrader dashboard
    volumes:
      - ./data:/app/data
    ports:
      - "18080:8080"
    restart: unless-stopped
```

### Dockerfile

Multi-stage build:

```
Stage 1: golang:1.22-alpine
  ├── CGO_ENABLED=1 (required for go-sqlite3)
  ├── go build -o vagustrader ./cmd/vagustrader
  └── Result: static binary with embedded assets

Stage 2: alpine:3.19
  ├── ca-certificates (HTTPS)
  ├── vagustrader binary
  └── Run as CMD
```

### Commands

```bash
# Build
docker compose build

# Deploy (build + start)
docker compose up -d --build

# Start specific service
docker compose up -d bot
docker compose up -d dashboard

# View logs
docker compose logs bot -f
docker compose logs dashboard -f

# Execute commands inside container
docker compose exec bot vagustrader status
docker compose exec bot vagustrader simulate-settlement

# Stop
docker compose down
```

---

## Directory Structure

```
/path/to/deploy/
├── .env                  # Secrets (never commit)
├── config.yml            # Trading configuration
├── docker-compose.yml    # Service definitions
├── data/                 # SQLite database (persistent)
│   ├── vagustrader.db
│   ├── vagustrader.db-wal
│   └── vagustrader.db-shm
└── logs/                 # Rotating JSON logs
    └── vagustrader.log
```

### Volume Mounts

| Host Path | Container Path | Purpose |
|---|---|---|
| `./config.yml` | `/app/config.yml:ro` | Read-only config |
| `./data` | `/app/data` | SQLite database (shared) |
| `./logs` | `/app/logs` | Application logs |

---

## Environment Setup

### `.env` file

```env
PRIVATE_KEY=0x...
CLOB_API_KEY=
CLOB_API_SECRET=
CLOB_API_PASSPHRASE=
TELEGRAM_BOT_TOKEN=
TELEGRAM_CHANNEL_ID=
```

### Apply Changes

After modifying `.env` or `config.yml`:

```bash
docker compose restart bot
```

---

## Logging

| Aspect | Detail |
|---|---|
| Format | JSON (slog) |
| Output | stderr + rotating file |
| File path | `logs/vagustrader.log` |
| Rotation | 10MB per file |
| Backups | 5 rotating files |
| Level | INFO (configurable in `logging.Setup()`) |

Logs are written synchronously via `io.MultiWriter`. The JSON format is compatible with log aggregation tools (Loki, Splunk, Elasticsearch).

---

## Dashboard Access

The dashboard runs on `:8080` inside the container, mapped to `:18080` on the host by default.

### Production Considerations

| Concern | Recommendation |
|---|---|
| **Authentication** | Dashboard has no built-in auth. Use a reverse proxy (nginx/Caddy) for production. |
| **HTTPS** | Terminate TLS at reverse proxy. |
| **Public exposure** | Only expose if you add auth middleware. Not designed for multi-user access. |
| **Health checks** | Dashboard root (`/`) returns 200. Use as a health check endpoint. |

### Reverse Proxy Example (nginx)

```nginx
server {
    listen 443 ssl;
    server_name vagustrader.example.com;

    location / {
        proxy_pass http://127.0.0.1:18080;
        proxy_set_header Host $host;
    }

    # Optional: basic auth
    auth_basic "Restricted";
    auth_basic_user_file /etc/nginx/.htpasswd;
}
```

---

## Database Maintenance

SQLite in WAL mode handles concurrent access well, but:

| Task | Command |
|---|---|
| **Checkpoint WAL** | `sqlite3 data/vagustrader.db "PRAGMA wal_checkpoint(FULL);"` |
| **Vacuum** | `sqlite3 data/vagustrader.db "VACUUM;"` |
| **Backup** | `cp data/vagustrader.db backup/$(date +%Y%m%d).db` |
| **Integrity check** | `sqlite3 data/vagustrader.db "PRAGMA integrity_check;"` |

> The database is hot-backup safe only during low-activity periods (when no tick is running).

---

## Updating

```bash
# Pull latest code
git pull

# Rebuild and restart
docker compose up -d --build

# Check logs for startup
docker compose logs bot --tail 20
```

---

## Monitoring

### What to Watch

| Signal | Source | Alert |
|---|---|---|
| Bot process health | Docker status | Container not running |
| Last tick | Logs (`Tick end`) | No tick for >5 min |
| Balance change | Dashboard Realized PnL | Unexpected drop |
| DB file size | Filesystem | Growing too large |
| API errors | Logs (`error` level) | Rate limiting or auth failure |

### Docker Health Check

Add to docker-compose.yml:

```yaml
bot:
  healthcheck:
    test: ["CMD", "vagustrader", "status"]
    interval: 60s
    timeout: 10s
    retries: 3
```
