# 本地测试检查清单 - 上云前必读

只保留核心 API 与爬虫功能验证步骤，已移除本地 Prometheus/Grafana 相关内容。

---

## 步骤 1: 启动核心服务

```bash
docker compose up -d
docker compose ps
```

应看到：
- mysql / redis / api / crawler / nginx 都是 Up

---

## 步骤 2: 健康检查

```bash
curl http://localhost:8081/healthz
```

应返回：
```
{"status":"ok"}
```

---

## 步骤 3: 任务闭环验证

1) 注册或登录获取 token  
2) 创建任务  
3) 查询任务与时间线  

```bash
# 注册
curl -X POST http://localhost:8081/api/auth/register \
  -H "Content-Type: application/json" \
  -d '{
    "username": "test_user",
    "email": "test@example.com",
    "password": "test123456",
    "invite_code": "3724"
  }'

# 登录
TOKEN=$(curl -X POST http://localhost:8081/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{
    "email": "test@example.com",
    "password": "test123456"
  }' | jq -r '.token')

# 创建任务
curl -X POST http://localhost:8081/api/tasks \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "keyword": "测试商品",
    "platform": 1,
    "min_price": 100,
    "max_price": 1000
  }'

# 查询任务
curl -H "Authorization: Bearer $TOKEN" http://localhost:8081/api/tasks

# 查询时间线
curl -H "Authorization: Bearer $TOKEN" http://localhost:8081/api/timeline
```

---

## 步骤 4: 基础日志检查

```bash
docker compose logs -f api
docker compose logs -f crawler
```

确认：
- scheduler 启动
- crawler gRPC 启动
- 任务能被调度执行
