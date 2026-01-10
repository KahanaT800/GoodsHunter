# 部署模式说明

GoodsHunter 目前仅保留基础部署与 Grafana Cloud 云端监控模式，不再部署本地 Prometheus/Grafana。

**重要**: 请使用 `docker compose`（v2）而不是 `docker-compose`（v1）

---

## 模式选择

### 模式 1: 最小模式（2GB 服务器）

**包含服务**：
- mysql（数据库）
- redis（缓存 + 队列）
- api（API 服务）
- crawler（爬虫服务）
- nginx（反向代理）

**启动命令**：
```bash
docker compose up -d
```

---

### 模式 2: + SSL（2GB 服务器 + 自己域名）

**额外服务**：
- certbot（Let's Encrypt SSL 自动续期）

**启动命令**：
```bash
docker compose --profile ssl up -d
```

---

### 模式 3: 云端监控（Grafana Cloud）

**说明**：通过 Grafana Cloud 采集日志与指标，不再部署本地监控面板。

**额外服务**：
- alloy（采集器）
- cadvisor / node-exporter / exporters（容器与主机指标）

**启动命令**：
```bash
docker compose --profile monitoring-cloud up -d
```

**必需环境变量**（见 `.env.example`）：
- `GRAFANA_CLOUD_PROM_REMOTE_WRITE_URL`
- `GRAFANA_CLOUD_PROM_USERNAME`
- `GRAFANA_CLOUD_PROM_API_KEY`
- `GRAFANA_CLOUD_LOKI_URL`
- `GRAFANA_CLOUD_LOKI_USERNAME`
- `GRAFANA_CLOUD_LOKI_API_KEY`

---

## 服务管理

### 查看运行的服务
```bash
docker compose ps
docker compose --profile ssl ps
docker compose --profile monitoring-cloud ps
```

### 查看日志
```bash
docker compose logs -f api
docker compose logs -f crawler
```

### 重启服务
```bash
docker compose restart api
docker compose restart crawler
docker compose restart nginx
```

### 停止服务
```bash
docker compose down
```
