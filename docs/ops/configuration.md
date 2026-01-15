# GoodsHunter Configuration Reference

Complete environment variable reference and system capacity planning guide.

---

## Environment Variables

### Core Settings

| Variable | Description | Default |
|----------|-------------|---------|
| `APP_ENV` | Runtime environment: `local` / `prod` | `local` |
| `APP_LOG_LEVEL` | Log level: `debug` / `info` / `warn` / `error` | `info` |
| `APP_HTTP_ADDR` | API server listen address | `:8081` |
| `APP_SCHEDULE_INTERVAL` | Task scheduling interval (e.g., `5m`) | `5m` |
| `APP_NEW_ITEM_DURATION` | Duration for "new" item highlight | `1h` |
| `APP_GUEST_IDLE_TIMEOUT` | Guest session idle timeout | `10m` |
| `APP_GUEST_HEARTBEAT` | Guest heartbeat interval | `5m` |
| `APP_MAX_TASKS_PER_USER` | Max tasks per user | `3` |
| `APP_MAX_ITEMS_PER_TASK` | Max items retained per task | `300` |

### Redis & Distributed Queue

| Variable | Description | Default |
|----------|-------------|---------|
| `REDIS_ADDR` | **Required on Worker**. Master Redis address | `redis:6379` |
| `REDIS_PASSWORD` | Redis authentication password | (Empty) |
| `WORKER_ID` | Unique node identifier (e.g., `worker-01`) | `worker-01` |
| `JANITOR_INTERVAL` | Janitor scan interval for stuck tasks | `10m` |
| `JANITOR_TIMEOUT` | Task stuck threshold before rescue | `30m` |

### Rate Limiting

| Variable | Description | Default |
|----------|-------------|---------|
| `APP_RATE_LIMIT` | Requests per second (Token Bucket rate) | `3` |
| `APP_RATE_BURST` | Max burst requests allowed | `5` |
| `APP_DEDUP_WINDOW` | URL deduplication window (seconds) | `3600` |

### Proxy & Network

| Variable | Description | Default |
|----------|-------------|---------|
| `HTTP_PROXY` / `BROWSER_PROXY_URL` | Proxy URL for browser connections | (Empty) |
| `PROXY_AUTO_SWITCH` | Enable automatic proxy switching on failures | `false` |
| `PROXY_FAILURE_THRESHOLD` | Consecutive failures before switching to proxy | `10` |
| `PROXY_COOLDOWN` | Duration to stay in proxy mode after switching | `10m` |
| `FORCE_PROXY_ONCE` | Force proxy mode for next crawl (debug) | `false` |

### Browser Settings

| Variable | Description | Default |
|----------|-------------|---------|
| `CHROME_BIN` | Path to Chrome/Chromium binary | (Auto-detect) |
| `BROWSER_HEADLESS` | Run Chrome in headless mode | `true` |
| `BROWSER_MAX_CONCURRENCY` | Max concurrent browser pages | `3` |
| `BROWSER_MAX_FETCH_COUNT` | Max items to fetch per crawl | `50` |
| `BROWSER_PAGE_TIMEOUT` | Page load/element wait timeout | `2m` |
| `BROWSER_DEBUG_SCREENSHOT` | Save screenshots on timeout/block | `false` |

### Worker Lifecycle

| Variable | Description | Default |
|----------|-------------|---------|
| `MAX_TASKS` | Tasks before worker self-restart (memory leak prevention) | `500` |

### Database

| Variable | Description | Default |
|----------|-------------|---------|
| `DB_DSN` | Full MySQL DSN (overrides individual settings) | (See config.json) |
| `DB_HOST` | MySQL host | `localhost` |
| `DB_PORT` | MySQL port | `3306` |
| `DB_USER` | MySQL user | `root` |
| `DB_PASSWORD` | MySQL password | (Empty) |
| `DB_NAME` | MySQL database name | `goodshunter` |

### Email Notifications

| Variable | Description | Default |
|----------|-------------|---------|
| `SMTP_HOST` | SMTP server host | `smtp.gmail.com` |
| `SMTP_PORT` | SMTP server port | `587` |
| `SMTP_USER` | SMTP username | (Empty) |
| `SMTP_PASS` | SMTP password | (Empty) |
| `SMTP_FROM` | Sender email address | (Empty) |

### Security

| Variable | Description | Default |
|----------|-------------|---------|
| `JWT_SECRET` | JWT signing secret | `dev_secret_change_me` |
| `INVITE_CODE` / `APP_INVITE_CODE` | Registration invite code (empty = disabled) | (Empty) |

### Observability (Grafana Cloud)

| Variable | Description | Default |
|----------|-------------|---------|
| `GRAFANA_CLOUD_PROM_REMOTE_WRITE_URL` | Prometheus remote write endpoint | (Empty) |
| `GRAFANA_CLOUD_PROM_USERNAME` | Prometheus username | (Empty) |
| `GRAFANA_CLOUD_PROM_API_KEY` | Prometheus API key | (Empty) |
| `GRAFANA_CLOUD_LOKI_URL` | Loki push endpoint | (Empty) |
| `GRAFANA_CLOUD_LOKI_USERNAME` | Loki username | (Empty) |
| `GRAFANA_CLOUD_LOKI_API_KEY` | Loki API key | (Empty) |

---

## Redis Keys Reference

GoodsHunter uses Redis for message queuing and distributed state coordination.

### Message Queues

| Key | Type | Purpose |
|-----|------|---------|
| `goodshunter:queue:tasks` | List | Pending task queue |
| `goodshunter:queue:tasks:processing` | List | Processing backup (reliable queue) |
| `goodshunter:queue:results` | List | Result queue |
| `goodshunter:queue:tasks:pending` | Set | Task ID deduplication set |
| `goodshunter:queue:tasks:started` | Hash | Task start timestamps (for Janitor) |

### Distributed State

| Key | Type | Purpose |
|-----|------|---------|
| `goodshunter:ratelimit:global` | String | Token Bucket state (Lua script) |
| `goodshunter:cookies:mercari` | String | Cached session cookies (JSON) |
| `goodshunter:crawler:consecutive_blocks` | String | Block event counter (adaptive throttling) |
| `goodshunter:crawler:consecutive_failures` | String | Failure counter (proxy switching) |
| `goodshunter:proxy:cooldown` | String | Proxy mode cooldown timestamp |
| `goodshunter:dedup:url:*` | String | URL deduplication with TTL |

---

## System Capacity Planning

This section describes processing capacity, bottlenecks, and scaling considerations.

### Concurrency Model

```
┌─────────────────────────────────────────────────────────┐
│              Concurrency Hierarchy                       │
├─────────────────────────────────────────────────────────┤
│  Semaphore (BROWSER_MAX_CONCURRENCY)                    │
│       │                                                  │
│       ▼                                                  │
│  Rate Limiter (APP_RATE_LIMIT + APP_RATE_BURST)         │
│       │                                                  │
│       ▼                                                  │
│  Browser Pages (actual concurrent crawls)               │
└─────────────────────────────────────────────────────────┘
```

The crawler uses a semaphore-based concurrency control where `BROWSER_MAX_CONCURRENCY` limits the number of simultaneous browser pages.

### Bottleneck Analysis

| Constraint | Config | Effect |
|------------|--------|--------|
| Rate Limit | `APP_RATE_LIMIT=3` | 3 tokens/second |
| Rate Burst | `APP_RATE_BURST=5` | Short burst allowance |
| Browser Pages | `BROWSER_MAX_CONCURRENCY=3` | Max simultaneous pages |

**Peak concurrency** = min(`APP_RATE_BURST`, `BROWSER_MAX_CONCURRENCY`) = **3-5 concurrent crawls**

### Timeout Configuration

| Timeout | Value | Description |
|---------|-------|-------------|
| Task timeout | 90s | Single task max execution time |
| Watchdog timeout | 100s | Failsafe task termination |
| Page timeout | 2m | Browser page load timeout |
| Rate limit wait | 30s max | Max wait for rate limiter token |

### Capacity Summary

| Metric | Value |
|--------|-------|
| Throughput ceiling | ~3 tasks/second |
| Per-minute capacity | ~180 tasks/min |
| Suitable for | Single-node or distributed deployment |

### Memory Considerations

| Instance Type | Browser Concurrency | Notes |
|---------------|---------------------|-------|
| t3.micro (1GB) | 1-2 | Minimal, dev only |
| t3.small (2GB) | 3-5 | Production baseline |
| t3.medium (4GB) | 5-10 | Comfortable headroom |

**Warning**: `BROWSER_MAX_CONCURRENCY=5` on t3.small (2GB) is near the limit. Do not increase without memory upgrade.

---

## Configuration Examples

### Minimal Worker `.env`

```ini
REDIS_ADDR=master-ip:6379
REDIS_PASSWORD=your_password
WORKER_ID=worker-01
```

### Production Worker `.env`

```ini
# Redis Connection
REDIS_ADDR=master-ip:6379
REDIS_PASSWORD=your_strong_password

# Worker Identity
WORKER_ID=home-pc-01

# Browser Settings
BROWSER_HEADLESS=true
BROWSER_MAX_CONCURRENCY=3
MAX_TASKS=500

# Rate Limiting
APP_RATE_LIMIT=3
APP_RATE_BURST=5

# Observability
GRAFANA_CLOUD_PROM_REMOTE_WRITE_URL=https://prometheus-blocks-prod-...
GRAFANA_CLOUD_PROM_USERNAME=123456
GRAFANA_CLOUD_PROM_API_KEY=glc_...
```

### Debug Mode `.env`

```ini
APP_LOG_LEVEL=debug
BROWSER_HEADLESS=false
BROWSER_DEBUG_SCREENSHOT=true
FORCE_PROXY_ONCE=true
```
