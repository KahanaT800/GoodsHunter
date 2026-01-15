# GoodsHunter Architecture & Design

> A Go microservices-based real-time ACGN merchandise monitoring system with task-driven crawling, incremental deduplication, and multi-channel notifications.

---

## 1. Background & Goals

### 1.1 Problem Statement

ACGN merchandise (anime/game collectibles) on second-hand platforms like Mercari has these characteristics:

- **Short lifecycle**: Low-priced items sell within minutes
- **SPA architecture**: Traditional static crawlers cannot handle dynamic content
- **Manual inefficiency**: Users must constantly refresh and search manually

### 1.2 Project Goals

GoodsHunter aims to build a **task-driven, real-time, scalable** monitoring system:

- Abstract user intent into persistent **monitoring tasks**
- Perform **periodic crawling with incremental deduplication**
- Deliver actionable information via **Timeline UI + notifications**
- Demonstrate **Go backend engineering and microservices architecture**

### 1.3 Non-Goals

- Full platform coverage (Mercari first, extensible design)
- Transaction/payment processing (information aggregation only)
- Complex AI prediction (deterministic rule engine only)

---

## 2. System Architecture

### 2.1 Design Principles

| Principle | Description |
|-----------|-------------|
| **Control/Compute Separation** | API service does not run browser instances |
| **Task-Driven Model** | All crawling triggered by task scheduler |
| **Stateless Crawlers** | Crawler nodes can scale horizontally or restart freely |
| **Incremental Processing** | Only process new or changed items |

### 2.2 Component Overview

| Component | Responsibility |
|-----------|----------------|
| Web Frontend | User interaction, task management, timeline display |
| API Service | Business logic, auth, scheduling, result processing, notifications |
| Crawler Service | Page rendering, DOM parsing, deduplication |
| MySQL | Persistent storage for users, tasks, items |
| Redis | Message queues, distributed state, caching |
| Notification Channels | Email, Telegram, Discord |

### 2.3 Data Flow

```
1. User creates/starts monitoring task via frontend
2. API records task config and adds to scheduler queue
3. Scheduler triggers Crawler via Redis queue
4. Crawler renders page, parses items, checks dedup
5. New/changed items returned to API via result queue
6. API persists data, updates timeline, triggers notifications
```

### 2.4 Key Processing Flows

**Task Creation:**
```
POST /tasks → Validate params → Create task record → Return task ID
```

**Scheduling:**
```
Scheduler scans running tasks → Push to Redis queue → Worker picks up
```

**Crawling:**
```
Worker receives task → Open page → Scroll/wait → Parse DOM → Dedup check → Push result
```

**Result Processing:**
```
API pops result → Upsert items → Update timeline → Send notifications
```

---

## 3. Distributed Architecture (V2.0+)

### 3.1 Master-Worker Topology

```
┌─────────────────────────────────────────────────────────────┐
│                      Master Node                             │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────┐  │
│  │ API Service │  │   MySQL     │  │       Redis         │  │
│  │ + Scheduler │  │             │  │ Queues + State      │  │
│  └─────────────┘  └─────────────┘  └─────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
                              │
              ┌───────────────┼───────────────┐
              ▼               ▼               ▼
        ┌──────────┐    ┌──────────┐    ┌──────────┐
        │ Worker 1 │    │ Worker 2 │    │ Worker N │
        │ (Crawler)│    │ (Crawler)│    │ (Crawler)│
        └──────────┘    └──────────┘    └──────────┘
```

### 3.2 Redis Data Structures

**Message Queues:**

| Key | Type | Purpose |
|-----|------|---------|
| `goodshunter:queue:tasks` | List | Pending task queue |
| `goodshunter:queue:tasks:processing` | List | Processing backup (reliable queue) |
| `goodshunter:queue:results` | List | Result queue |
| `goodshunter:queue:tasks:pending` | Set | Task ID deduplication |
| `goodshunter:queue:tasks:started` | Hash | Task start timestamps |

**Distributed State:**

| Key | Type | Purpose |
|-----|------|---------|
| `goodshunter:ratelimit:global` | String | Token bucket state (Lua script) |
| `goodshunter:cookies:mercari` | String | Session cookie cache |
| `goodshunter:crawler:consecutive_blocks` | String | Block event counter |
| `goodshunter:proxy:cooldown` | String | Proxy mode cooldown |
| `goodshunter:dedup:url:*` | String | URL deduplication with TTL |

### 3.3 Reliable Queue Pattern

```
1. Worker: BRPOPLPUSH tasks → processing (atomic)
2. Worker: Process task
3. Worker: LPUSH results
4. Worker: LREM processing (ACK)
5. Janitor: Rescue stuck tasks from processing → tasks
```

---

## 4. Anti-Detection System (V2.1)

### 4.1 Request Pipeline

```
┌─────────────────────────────────────────────────────────────┐
│                    Request Pipeline                          │
├─────────────────────────────────────────────────────────────┤
│  1. Rate Limiter (Redis Lua Script - Token Bucket)          │
│  2. Request Jitter (800ms ~ 3.5s random delay)              │
│  3. Cookie Injection (Redis-cached session cookies)         │
│  4. Stealth Scripts (go-rod/stealth + enhanced patches)     │
│  5. Human Behavior Simulation (mouse move + scroll)         │
│  6. Block Detection & Classification                         │
│  7. Adaptive Throttling (auto-sleep on consecutive blocks)  │
└─────────────────────────────────────────────────────────────┘
```

### 4.2 Stealth Script Coverage

| Detection Vector | Mitigation |
|------------------|------------|
| `navigator.webdriver` | Deleted from prototype |
| `navigator.plugins` | Simulated with realistic plugin list |
| `chrome.runtime` / `chrome.csi` | Patched for headless mode |
| WebGL Renderer | Spoofed to "Intel Iris OpenGL Engine" |
| Selenium/Puppeteer traces | Removed 20+ known detection properties |
| iframe `contentWindow` | Patched to include `chrome` object |
| Function `toString()` | Overridden to return `[native code]` |

### 4.3 Human Behavior Simulation

- **Mouse Movement**: Random coordinates within viewport
- **Smooth Scrolling**: 200-500px random distance
- **Request Jitter**: 800ms–3.5s delay between requests

### 4.4 Cookie Persistence

| Feature | Description |
|---------|-------------|
| Cache Location | Redis with 30min TTL |
| Reuse Strategy | Load before request, save after success |
| Refresh Policy | 5% random refresh + 2h max age |
| Invalidation | Clear on 403 detection |

### 4.5 Adaptive Throttling

When consecutive blocks are detected:

1. Increment counter in Redis (cross-node sync)
2. After 3 consecutive blocks → enter cooldown (30-60s)
3. Reset counter on successful request

### 4.6 Block Detection & Classification

| Block Type | Detection Signals |
|------------|-------------------|
| `cloudflare_challenge` | JS challenge, Turnstile, challenge iframe |
| `captcha` | reCAPTCHA, hCAPTCHA elements |
| `403_forbidden` | 403 status, "Access Denied" text |
| `429_rate_limited` | 429 status, "Too Many Requests" |
| `blank_page` | Empty title, minimal content |
| `connection_error` | ERR_PROXY, ERR_CONNECTION |
| `js_loading_stuck` | Header exists but no items loaded |

---

## 5. Task Lifecycle & Reliability

### 5.1 Task State Machine

```
pending → running → paused → stopped
              ↓
           failed (after N consecutive failures)
```

### 5.2 Watchdog Mechanism

```
Task Received → Semaphore Acquire → Watchdog Start (100s)
                                          │
                               FetchItems (90s timeout)
                                          │
                          ┌───────────────┴───────────────┐
                          ↓                               ↓
                      Success                          Timeout
                          │                               │
                   Push Result                    Push Error Result
                   AckTask ✓                      (No Ack - Janitor rescues)
                          │                               │
                   Release Semaphore              Release Semaphore
```

### 5.3 Self-Healing Workers

- **Suicide on Quota**: Worker exits after `MAX_TASKS` (default: 500)
- **Docker Restart**: `restart: always` spins up fresh instance
- **Memory Leak Prevention**: Clean state on each restart

---

## 6. Data Model

### 6.1 MySQL Schema (Core Tables)

**users**
```sql
id, email, password_hash, created_at
```

**tasks**
```sql
id, user_id, keyword, exclude_keywords, 
min_price, max_price, interval_seconds,
status, last_run_at, last_success_at, last_error
```

**items**
```sql
id, source, source_item_id, title, price,
url, image_url, posted_at
UNIQUE INDEX (source, source_item_id)
```

**task_items** (Timeline)
```sql
id, task_id, item_id, event_type, 
seen, favorited, created_at
INDEX (task_id, created_at)
```

### 6.2 Deduplication Strategy

```
1. Generate item_key = source + source_item_id
2. Check Redis dedup set
3. If not exists → Add to set, return as new
4. If exists → Check price change, return event type
```

---

## 7. Notification System

### 7.1 Trigger Conditions

- New item matching task criteria
- Price drop exceeding threshold (configurable, default 5%)

### 7.2 Channels

| Channel | Implementation |
|---------|----------------|
| Email | SMTP |
| Telegram | Bot API |
| Discord | Webhook |

### 7.3 Rate Limiting

- Same task + same item: 30min minimum interval
- Batch multiple price drops into single notification

---

## 8. Technology Stack

| Domain | Technology |
|--------|------------|
| Language | Go 1.23+ |
| Web Framework | Gin |
| Browser Automation | go-rod |
| Database | MySQL 8.0 |
| Cache/Queue | Redis 7.x |
| Configuration | Viper |
| Observability | Grafana Alloy (Prometheus + Loki) |
| Deployment | Docker / Docker Compose |

---

## 9. Project Structure

```
.
├── cmd/
│   ├── api/              # API service entrypoint
│   └── crawler/          # Crawler service entrypoint
├── internal/
│   ├── api/              # API handlers, middleware
│   ├── crawler/          # Browser automation, parsing
│   ├── config/           # Configuration loading
│   ├── model/            # Data models
│   └── pkg/              # Shared utilities
│       ├── dedup/        # Deduplication logic
│       ├── logger/       # Structured logging
│       ├── metrics/      # Prometheus metrics
│       ├── notify/       # Notification channels
│       ├── queue/        # In-memory queue
│       ├── ratelimit/    # Distributed rate limiter
│       └── redisqueue/   # Redis queue client
├── proto/                # Protobuf definitions
├── web/                  # Frontend assets
├── configs/              # Configuration examples
├── deploy/               # Deployment configs (Alloy)
└── docs/                 # Documentation
```

---

## 10. Future Directions

- **Multi-Platform**: Yahoo Auction, Surugaya, Mandarake
- **Event Streaming**: Kafka / Redis Streams for async processing
- **Rule DSL**: User-defined matching rules
- **Recommendation**: Tag-based and behavior-based suggestions

---

## 11. Summary

GoodsHunter is a **task-driven, event-oriented** merchandise monitoring system featuring:

- Clear separation of concerns (API / Scheduler / Crawler)
- Reliable distributed task queue with zero-loss guarantee
- Multi-layer anti-detection with adaptive throttling (V2.1)
- Full observability with Grafana Cloud integration

The project demonstrates production-grade Go backend engineering and microservices architecture.
