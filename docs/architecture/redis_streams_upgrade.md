# Redis Streams 任务队列升级指南

## 概述

本次升级引入了 Redis Streams 作为任务队列的持久化层，在保留原有功能的基础上提供：
- 任务持久化（重启不丢失）
- 秒级响应（创建即执行）
- 分布式支持（多实例负载均衡）
- 平滑切换（开关控制，兼容旧模式）

## 架构对比

### 当前架构（默认）
```
用户创建任务 → MySQL → 等待5分钟 → Scheduler轮询 → 执行
```

### 升级后架构（启用 Redis Streams）
```
用户创建任务 → MySQL + Redis → 秒级执行
              ↓
        定时发布器 → Redis → 周期执行（24小时监控）
```

## 使用方法

### 方式 1：配置文件控制（推荐）

编辑 `configs/config.json`:

```json
{
  "app": {
    "enable_redis_queue": true,
    "task_queue_stream": "goodshunter:task:queue",
    "task_queue_group": "scheduler_group"
  }
}
```

### 方式 2：环境变量控制

```bash
# 启用 Redis Streams
export APP_ENABLE_REDIS_QUEUE=true

# 可选：自定义 Stream 名称
export APP_TASK_QUEUE_STREAM=goodshunter:task:queue
export APP_TASK_QUEUE_GROUP=scheduler_group
```

### 方式 3：Docker Compose

在 `docker-compose.yml` 中：

```yaml
api:
  environment:
    APP_ENABLE_REDIS_QUEUE: "true"
    APP_TASK_QUEUE_STREAM: "goodshunter:task:queue"
    APP_TASK_QUEUE_GROUP: "scheduler_group"
```

## 测试验证

### 1. 测试默认模式（关闭 Redis Streams）

```bash
# 确保配置中 enable_redis_queue 为 false 或未设置
make run

# 观察日志，应该看到：
# scheduler running in DB polling mode
```

### 2. 测试 Redis Streams 模式

```bash
# 方式 1：修改 config.json
vim configs/config.json
# 设置 "enable_redis_queue": true

# 方式 2：环境变量
export APP_ENABLE_REDIS_QUEUE=true

make run

# 观察日志，应该看到：
# scheduler running in Redis Streams mode
# periodic publisher started
```

### 3. 创建任务测试

```bash
# 注册并登录
curl -X POST http://localhost:8080/register \
  -H "Content-Type: application/json" \
  -d '{"email":"test@example.com","password":"test123"}'

# 获取 token（从登录响应中）
TOKEN="your_jwt_token"

# 创建任务
curl -X POST http://localhost:8080/tasks \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "keyword": "iPhone 15",
    "platform": 1,
    "min_price": 0,
    "max_price": 100000,
    "sort": "created_time|desc"
  }'

# Redis Streams 模式：几秒内开始执行
# 轮询模式：最多等待 5 分钟
```

### 4. 查看 Redis 队列状态

```bash
# 进入 Redis
redis-cli

# 查看 Stream 长度
XLEN goodshunter:task:queue

# 查看消费者组信息
XINFO GROUPS goodshunter:task:queue

# 查看待处理消息
XPENDING goodshunter:task:queue scheduler_group

# 查看最新消息
XREAD COUNT 10 STREAMS goodshunter:task:queue 0
```

## 监控和调试

### 日志关键词

#### Redis Streams 模式
```
scheduler running in Redis Streams mode
task producer initialized
periodic publisher started
task submitted to redis queue
received messages from redis
```

#### 轮询模式
```
scheduler running in DB polling mode
redis queue disabled, using polling mode
```

### 性能对比测试

```bash
# 测试脚本：创建 10 个任务
for i in {1..10}; do
  curl -X POST http://localhost:8080/tasks \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"keyword\":\"test$i\",\"platform\":1}" &
done

# 观察日志中的执行时间
docker logs goodshunter-api-1 -f | grep "task run started"
```

**预期结果**：
- **轮询模式**：所有任务在下一个 5 分钟周期开始执行
- **Redis Streams 模式**：任务在 1-2 秒内开始执行

##  注意事项

### 兼容性
- 可以随时开启/关闭 Redis Streams 模式
- 两种模式共享相同的数据库和执行逻辑
- 切换模式不影响现有任务

### 降级策略
如果 Redis Streams 出现问题，系统会自动降级：
```
Redis 发布失败 → 自动切换到直接调度 → 记录警告日志
```

### 周期执行保证
无论哪种模式，任务的周期性执行都得到保障：
- **轮询模式**：每 5 分钟轮询数据库
- **Redis Streams 模式**：周期发布器每 5 分钟发布任务

## 下一步扩展

当前实现为未来的分布式爬虫预留了接口：

### 阶段 1（当前）：任务调度分布式
```
API → Redis → Scheduler（可多实例）→ Crawler（单实例）
```

### 阶段 2（未来）：爬虫执行分布式
```
API → Redis (task:queue)
         ↓
     Scheduler
         ↓
  Redis (crawl:queue)
         ↓
   多个 Crawler 实例（自动负载均衡）
```

## 故障排查

### 问题 1：Redis 连接失败
```
错误：failed to create task consumer
解决：检查 Redis 连接配置，系统会自动降级到轮询模式
```

### 问题 2：消息未被消费
```bash
# 检查消费者组状态
redis-cli XINFO GROUPS goodshunter:task:queue

# 查看待处理消息
redis-cli XPENDING goodshunter:task:queue scheduler_group
```

### 问题 3：任务重复执行
```
原因：周期发布器和手动创建都会发布任务
正常：每个任务只会被消费一次（Redis Streams 保证）
```

## 验收清单

- [ ] 默认模式（enable_redis_queue=false）正常运行
- [ ] Redis Streams 模式（enable_redis_queue=true）正常运行
- [ ] 创建任务后秒级开始执行（Redis 模式）
- [ ] 任务周期性执行不受影响
- [ ] Redis 故障时自动降级
- [ ] 日志中能看到正确的运行模式
- [ ] Redis 中能查询到队列数据

## 相关文档

- [Redis Streams 官方文档](https://redis.io/docs/data-types/streams/)
- [Worker Pool 设计文档](../docs/WORKER_POOL_INTEGRATION.md)
- [配置指南](./README.md)
