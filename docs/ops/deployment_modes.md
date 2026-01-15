# Deployment Modes

GoodsHunter supports multiple deployment configurations for different use cases.

> **Note**: Use `docker compose` (v2) instead of `docker-compose` (v1)

---

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│                     Master Node                              │
│  (Cloud Server - e.g., AWS t3.small)                        │
│                                                              │
│  ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐            │
│  │  MySQL  │ │  Redis  │ │   API   │ │  Nginx  │            │
│  └─────────┘ └─────────┘ └─────────┘ └─────────┘            │
│                    │                                         │
│              ┌─────┴─────┐                                   │
│              │  Crawler  │  (Optional - Master can crawl)   │
│              └───────────┘                                   │
└─────────────────────────────────────────────────────────────┘
                              │
                         Redis:6379
                              │
              ┌───────────────┼───────────────┐
              ▼               ▼               ▼
        ┌──────────┐    ┌──────────┐    ┌──────────┐
        │ Worker 1 │    │ Worker 2 │    │ Worker N │
        │ (Home PC)│    │ (Home PC)│    │  (Any)   │
        └──────────┘    └──────────┘    └──────────┘
```

---

## Deployment Modes

### Mode 1: Minimal (Single Node)

**Services**: MySQL, Redis, API, Crawler, Nginx

**Best for**: Development, testing, small-scale production

```bash
docker compose up -d
```

**Resource Requirements**: 2GB RAM minimum

---

### Mode 2: Minimal + SSL

**Additional Services**: Certbot (Let's Encrypt auto-renewal)

**Best for**: Production with custom domain

```bash
docker compose --profile ssl up -d
```

**Prerequisites**:
- Domain pointing to your server
- Ports 80/443 open

---

### Mode 3: Cloud Monitoring (Grafana Cloud)

**Additional Services**: Alloy, cAdvisor, Node Exporter

**Best for**: Production with full observability

```bash
docker compose --profile monitoring-cloud up -d
```

**Required Environment Variables**:
```ini
GRAFANA_CLOUD_PROM_REMOTE_WRITE_URL=https://prometheus-...
GRAFANA_CLOUD_PROM_USERNAME=123456
GRAFANA_CLOUD_PROM_API_KEY=glc_...
GRAFANA_CLOUD_LOKI_URL=https://logs-...
GRAFANA_CLOUD_LOKI_USERNAME=123456
GRAFANA_CLOUD_LOKI_API_KEY=glc_...
```

---

### Mode 4: Master + Remote Workers (Distributed)

**Master Node**: API, MySQL, Redis, Nginx (+ optional Crawler)

**Worker Nodes**: Crawler only

**Best for**: High-throughput production, leveraging home PCs

#### Master Setup

```bash
# On cloud server
docker compose up -d
```

**Important**: Open Redis port (6379) to worker IPs only!

#### Worker Setup

```bash
# On each worker machine
docker compose -f docker-compose.worker.yml up -d
```

**Worker `.env`**:
```ini
REDIS_ADDR=<master-ip>:6379
REDIS_PASSWORD=<your-password>
WORKER_ID=worker-home-01
```

---

## Profile Combinations

| Profile Command | Services Started |
|-----------------|------------------|
| `docker compose up -d` | mysql, redis, api, crawler, nginx |
| `docker compose --profile ssl up -d` | + certbot |
| `docker compose --profile monitoring-cloud up -d` | + alloy, cadvisor, node-exporter |
| `docker compose --profile ssl --profile monitoring-cloud up -d` | All services |

---

## Service Management

### View Running Services

```bash
docker compose ps
docker compose --profile monitoring-cloud ps
```

### View Logs

```bash
# Follow specific service logs
docker compose logs -f api
docker compose logs -f crawler

# Last 100 lines
docker compose logs --tail 100 crawler
```

### Restart Services

```bash
docker compose restart api
docker compose restart crawler
docker compose restart nginx

# Restart with rebuild
docker compose up -d --build api
```

### Stop Services

```bash
# Stop all
docker compose down

# Stop and remove volumes (⚠️ deletes data!)
docker compose down -v
```

### Update Deployment

```bash
git pull
docker compose pull
docker compose up -d --build
```

---

## Network Security

### Master Node Firewall

| Port | Service | Access |
|------|---------|--------|
| 80 | HTTP | Public |
| 443 | HTTPS | Public |
| 6379 | Redis | Worker IPs only! |
| 3306 | MySQL | Internal only |

### AWS Security Group Example

```
Inbound Rules:
- TCP 80   : 0.0.0.0/0 (HTTP)
- TCP 443  : 0.0.0.0/0 (HTTPS)
- TCP 6379 : <your-home-ip>/32 (Redis - Workers only)
- TCP 22   : <your-ip>/32 (SSH)
```

---

## Troubleshooting

### Worker Cannot Connect to Redis

```bash
# Test Redis connectivity from worker
redis-cli -h <master-ip> -p 6379 -a <password> ping
```

**Common issues**:
- Firewall blocking port 6379
- Wrong `REDIS_ADDR` or `REDIS_PASSWORD`
- Redis not bound to external interface

### Crawler Keeps Restarting

Check logs for the cause:
```bash
docker compose logs --tail 200 crawler
```

**Common issues**:
- Out of memory (reduce `BROWSER_MAX_CONCURRENCY`)
- Chrome crash (add `--no-sandbox` flag)
- `MAX_TASKS` reached (normal self-healing behavior)

### High Memory Usage

```bash
# Check container memory
docker stats

# Reduce browser concurrency
BROWSER_MAX_CONCURRENCY=2 docker compose up -d crawler
```
