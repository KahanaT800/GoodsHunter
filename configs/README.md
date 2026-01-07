# GoodsHunter 配置指南 (Configuration)

GoodsHunter 支持通过 **JSON 配置文件** 和 **环境变量** 两种方式进行配置。系统加载顺序为：**默认值 -> 配置文件 (config.json) -> 环境变量**（优先级最高）。

## 📂 文件结构

| 文件名 | 说明 | Git 提交状态 |
|--------|------|--------------|
| `config.json.example` | 配置模板，包含完整字段 | ✅ 已提交 |
| `config.production.json` | 生产环境参考配置 | ✅ 已提交 |
| `config.yaml.example` | YAML 格式参考（需自行转换为 JSON） | ✅ 已提交 |
| `config.json` | **本地实际配置文件** | ❌ 忽略 (包含敏感信息) |

---

## 🚀 快速开始

### 方式一：使用配置文件（推荐本地开发）

1.  复制模板文件：
    ```bash
    cp configs/config.json.example configs/config.json
    ```
2.  修改  `configs/config.json` 中的关键配置（如数据库密码、SMTP 信息）。
3.  程序启动时会自动加载此文件。

### 方式二：使用环境变量（推荐 Docker/生产环境）

在 `docker-compose.yml` 或 `.env` 文件中设置对应环境变量，**无需** 挂载 `config.json`。

---

## ⚙️ 配置项对照表

### 核心服务 (App)

| JSON 字段 (`app.*`) | 环境变量 | 说明 | 默认值 |
|-------------------|----------|------|--------|
| `env` | `APP_ENV` | 运行环境 (`local`/`prod`) | `local` |
| `log_level` | `APP_LOG_LEVEL` | 日志级别 | `info` |
| `http_addr` | `APP_HTTP_ADDR` | API 服务端口 | `:8081` |
| `crawler_grpc_addr` | `APP_CRAWLER_GRPC_ADDR` | 爬虫服务地址 | `localhost:50051` |
| `schedule_interval` | `APP_SCHEDULE_INTERVAL` | 任务调度间隔 | `5m` |
| `worker_pool_size` | `APP_WORKER_POOL_SIZE` | 全局并发大小 | `50` |

### 数据库 (MySQL)

> 支持完整 DSN 字符串，或分拆字段配置

| JSON 字段 (`mysql.*`) | 环境变量 | 说明 |
|---------------------|----------|------|
| `dsn` | `DB_DSN` | 完整连接串 (最高优先级) |
| - | `DB_HOST` | 数据库主机 (如 `mysql`) |
| - | `DB_PORT` | 端口 (默认 `3306`) |
| - | `DB_USER` | 用户名 |
| - | `DB_PASSWORD` | **密码** |
| - | `DB_NAME` | 库名 (默认 `goodshunter`) |

### 缓存 (Redis)

| JSON 字段 (`redis.*`) | 环境变量 | 说明 |
|---------------------|----------|------|
| `addr` | `REDIS_ADDR` | 地址 (host:port) |
| `password` | `REDIS_PASSWORD` | **密码** |

### 爬虫引擎 (Browser)

| JSON 字段 (`browser.*`) | 环境变量 | 说明 |
|-----------------------|----------|------|
| `bin_path` | `CHROME_BIN` | Chrome 可执行文件路径 |
| `proxy_url` | `BROWSER_PROXY_URL` | 代理 (如 `http://127.0.0.1:7890`) |
| `headless` | `BROWSER_HEADLESS` | 是否无头模式 (`true`/`false`) |
| `max_concurrency` | `BROWSER_MAX_CONCURRENCY` | 单实例并发数 |

### 安全与鉴权 (Security)

| JSON 字段 (`security.*`) | 环境变量 | 说明 |
|------------------------|----------|------|
| `jwt_secret` | `JWT_SECRET` | 令牌签名密钥 |
| `invite_code` | `INVITE_CODE` | 注册邀请码 |

### 邮件通知 (Email)

| JSON 字段 (`email.*`) | 环境变量 | 说明 |
|---------------------|----------|------|
| `smtp_host` | `SMTP_HOST` | SMTP 服务器 |
| `smtp_port` | `SMTP_PORT` | SMTP 端口 |
| `smtp_user` | `SMTP_USER` | 发件账号 |
| `smtp_pass` | `SMTP_PASS` | **应用专用密码** |
| `from_email` | `FROM_EMAIL` | 发件人地址 |
| `to_email` | `TO_EMAIL` | 默认收件人 |

---

## 📝 完整配置示例 (config.json)

```json
{
  "app": {
    "env": "local",
    "http_addr": ":8081",
    "schedule_interval": "5m"
  },
  "mysql": {
    "dsn": "root:secret@tcp(mysql:3306)/goodshunter?charset=utf8mb4&parseTime=True&loc=Local"
  },
  "redis": {
    "addr": "redis:6379",
    "password": ""
  },
  "browser": {
    "headless": true,
    "max_concurrency": 5
  },
  "security": {
    "jwt_secret": "change-me-in-production"
  }
}
```

- 🔐 建议设置文件权限：`chmod 600 configs/config.json`
