# Local Testing Checklist

Essential verification steps before deploying to production.

---

## Step 1: Start Core Services

```bash
docker compose up -d
docker compose ps
```

**Expected**: All services (mysql, redis, api, crawler, nginx) show `Up` status.

---

## Step 2: Health Check

```bash
curl http://localhost:8081/healthz
```

**Expected Response**:
```json
{"status":"ok"}
```

---

## Step 3: End-to-End Task Verification

### Register & Login

```bash
# Register
curl -X POST http://localhost:8081/api/auth/register \
  -H "Content-Type: application/json" \
  -d '{
    "username": "test_user",
    "email": "test@example.com",
    "password": "test123456",
    "invite_code": "YOUR_INVITE_CODE"
  }'

# Login and get token
TOKEN=$(curl -s -X POST http://localhost:8081/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{
    "email": "test@example.com",
    "password": "test123456"
  }' | jq -r '.token')

echo $TOKEN
```

### Create & Query Task

```bash
# Create task
curl -X POST http://localhost:8081/api/tasks \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "keyword": "test item",
    "platform": 1,
    "min_price": 100,
    "max_price": 1000
  }'

# Query tasks
curl -H "Authorization: Bearer $TOKEN" http://localhost:8081/api/tasks

# Query timeline
curl -H "Authorization: Bearer $TOKEN" http://localhost:8081/api/timeline
```

---

## Step 4: Log Verification

```bash
# API logs
docker compose logs -f api

# Crawler logs
docker compose logs -f crawler
```

**Expected**:
- Scheduler started successfully
- Crawler gRPC server listening
- Tasks being scheduled and executed

---

## Step 5: Crawler Verification

Check that crawler is processing tasks:

```bash
# Watch crawler logs for task execution
docker compose logs -f crawler | grep -E "fetching|found items|crawl completed"
```

**Expected output pattern**:
```
fetching items task_id=xxx
found items count=N
crawl completed duration=Xs
```

---

## Step 6: Redis Queue Verification

```bash
# Connect to Redis
docker compose exec redis redis-cli

# Check queue lengths
LLEN goodshunter:queue:tasks
LLEN goodshunter:queue:results
LLEN goodshunter:queue:tasks:processing
```

---

## Troubleshooting

### API Not Responding

```bash
docker compose logs api | tail -50
docker compose restart api
```

### Crawler Not Processing Tasks

```bash
# Check if crawler is connected to Redis
docker compose logs crawler | grep -i redis

# Check rate limiter
docker compose exec redis redis-cli GET goodshunter:ratelimit:global
```

### Database Connection Issues

```bash
# Check MySQL status
docker compose exec mysql mysql -u root -p -e "SELECT 1"

# Check API database connection
docker compose logs api | grep -i mysql
```
