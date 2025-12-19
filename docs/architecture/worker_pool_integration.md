# Worker Pool 集成指南

## 概述

Worker Pool 已成功集成到 GoodsHunter Scheduler 中，通过固定数量的 goroutine 处理所有爬虫任务，避免无限制创建协程。

## 架构变化

### 之前（每任务一个 goroutine）
```
任务1 → Goroutine 1 → Ticker → 执行爬取
任务2 → Goroutine 2 → Ticker → 执行爬取
任务3 → Goroutine 3 → Ticker → 执行爬取
...
任务1000 → Goroutine 1000 → Ticker → 执行爬取
```
**问题**: 
- 1000 个任务 = 1000 个 goroutine
- 资源消耗随任务数线性增长
- 无并发控制

### 现在（Worker Pool）
```
┌────────────────────────────────────────┐
│         Scheduler (定时触发)             │
│    每 5 分钟扫描所有 running 任务        │
└────────────────┬───────────────────────┘
                 │
                 ▼
┌────────────────────────────────────────┐
│         任务队列 (容量 1000)             │
│     Task1, Task2, Task3, ... TaskN     │
└────────────────┬───────────────────────┘
                 │
                 ▼
┌────────────────────────────────────────┐
│      Worker Pool (50 个 Worker)        │
│  Worker1  Worker2  ...  Worker50       │
│     ↓        ↓            ↓            │
│  执行爬取  执行爬取    执行爬取          │
└────────────────────────────────────────┘
```
**优势**:
- 固定 50 个 goroutine，资源可控
- 自动排队，避免过载
- 并发限制，保护外部 API
- 优雅关闭，等待任务完成

## 配置参数

### 环境变量配置

在 `.env` 或环境变量中设置：

```bash
# Worker Pool 配置
APP_WORKER_POOL_SIZE=50    # 并发 Worker 数量（默认 50）
APP_QUEUE_CAPACITY=1000    # 队列容量（默认 1000）

# Scheduler 配置
APP_SCHEDULE_INTERVAL=5m   # 调度间隔（默认 5 分钟）
```

### 参数说明

#### `APP_WORKER_POOL_SIZE` - Worker 数量
- **推荐值**: 50-100
- **CPU 密集型**: `runtime.NumCPU()`
- **IO 密集型**: `runtime.NumCPU() * 2`
- **网络爬虫**: 根据外部 API 限速设置
- **示例**:
  - 小型项目 (< 50 任务): 20
  - 中型项目 (50-200 任务): 50
  - 大型项目 (> 200 任务): 100

#### `APP_QUEUE_CAPACITY` - Queue 容量
- **推荐值**: `Workers * 20` 到 `Workers * 50`
- **计算公式**: 
  - 容量 = Worker数 × 预期调度周期内的任务量
  - 例如: 50 Workers × 20 次/Worker = 1000
- **注意**: 容量过小会导致任务丢弃

## 监控与统计

### 自动日志输出

每分钟自动输出队列统计：

```
time=2026-01-05T21:45:00 level=INFO msg="queue statistics" 
    pending=15 
    capacity=1000 
    total_enqueued=1250 
    total_processed=1235 
    total_succeeded=1230 
    total_failed=5 
    total_dropped=0 
    total_panics=0
```

### 告警触发

当 `total_dropped > 100` 时自动告警：
```
level=WARN msg="high task drop rate detected, consider increasing workers or queue capacity" 
    total_dropped=150
```

### 手动查询统计

在代码中可以随时查询：
```go
stats := scheduler.queue.Stats()
fmt.Printf("队列长度: %d/%d\n", scheduler.queue.Len(), scheduler.queue.Cap())
fmt.Printf("总处理: %d, 成功: %d, 失败: %d\n", 
    stats.TotalProcessed, stats.TotalSucceeded, stats.TotalFailed)
```

## 工作流程

### 1. 系统启动
```go
// cmd/api/main.go
func main() {
    cfg, _ := config.Load()
    srv, _ := api.NewServer(ctx, cfg, logger)
    srv.Run()
}
```

### 2. 创建 Worker Pool
```go
// internal/api/server.go -> NewScheduler
sched := scheduler.NewScheduler(
    db, rdb, logger, crawlerClient,
    cfg.App.ScheduleInterval,  // 5m
    2*time.Minute,             // 超时
    cfg.App.WorkerPoolSize,    // 50 Workers
    cfg.App.QueueCapacity,     // 1000 capacity
)
```

### 3. 启动调度循环
```go
// internal/api/scheduler/scheduler.go -> Run()
queue.Start(ctx)                    // 启动 50 个 Worker
enqueueRunningTasks()               // 立即执行一次

ticker := time.NewTicker(5 * time.Minute)
for {
    case <-ticker.C:
        enqueueRunningTasks()       // 每 5 分钟入队所有 running 任务
}
```

### 4. 任务执行
```go
// Worker 自动从 Queue 取任务
func (q *Queue) worker(ctx context.Context, id int) {
    for job := range q.jobs {
        job(ctx)  // 执行 handleTaskByID()
                  //   → 查询数据库
                  //   → 调用爬虫 gRPC
                  //   → 处理商品列表
                  //   → 更新数据库
    }
}
```

### 5. 优雅关闭
```go
// 收到 SIGTERM 信号时
ctx.Done()
queue.ShutdownWithTimeout(30 * time.Second)  // 等待所有任务完成，最多 30 秒
```

## 性能优化建议

### 1. 根据任务数量调整 Worker 数
```bash
# 任务数 < 50
APP_WORKER_POOL_SIZE=20

# 任务数 50-200
APP_WORKER_POOL_SIZE=50

# 任务数 > 200
APP_WORKER_POOL_SIZE=100
```

### 2. 监控队列饱和度
- `pending / capacity < 0.8`: 正常
- `pending / capacity > 0.8`: 考虑增加 Worker 或容量
- `total_dropped > 0`: 立即增加容量

### 3. 调整调度间隔
```bash
# 快速更新（适合价格敏感商品）
APP_SCHEDULE_INTERVAL=2m

# 标准更新
APP_SCHEDULE_INTERVAL=5m

# 慢速更新（节省资源）
APP_SCHEDULE_INTERVAL=10m
```

### 4. 爬虫 API 限速保护
如果外部 API 有限速（如 100 req/min）：
```bash
# 限制并发数避免触发限速
APP_WORKER_POOL_SIZE=10   # 每分钟最多 10 个并发请求
```

## 故障排查

### 问题 1: 任务丢弃 (total_dropped > 0)
**原因**: 队列满，新任务无法入队
**解决**:
```bash
# 增加队列容量
APP_QUEUE_CAPACITY=2000

# 或增加 Worker 数量
APP_WORKER_POOL_SIZE=100
```

### 问题 2: 任务执行慢
**原因**: Worker 数量不足
**解决**:
```bash
# 增加并发 Worker
APP_WORKER_POOL_SIZE=100
```

### 问题 3: 内存占用高
**原因**: 队列容量过大
**解决**:
```bash
# 减少队列容量
APP_QUEUE_CAPACITY=500

# 或增加调度间隔
APP_SCHEDULE_INTERVAL=10m
```

### 问题 4: 频繁失败 (total_failed 增长)
**查看日志**:
```bash
grep "crawler task execution failed" logs/api.log
```
**常见原因**:
- 爬虫服务不可用
- 网络超时
- Redis/MySQL 连接问题

## 对比测试

### 场景: 100 个任务，每个任务耗时 10 秒

#### 旧架构（每任务一个 goroutine）
- Goroutine 数量: 100
- 内存占用: ~50MB (每个 goroutine ~500KB)
- 并发执行: 100 个同时执行
- 总耗时: ~10 秒（全部并发）

#### 新架构（Worker Pool）
- Goroutine 数量: 50 (固定)
- 内存占用: ~25MB
- 并发执行: 50 个同时执行
- 总耗时: ~20 秒（分两批）

**优势**: 
- 内存节省 50%
- 可控的并发数
- 避免资源耗尽
- 更好的稳定性

## 最佳实践

1. **生产环境配置**
   ```bash
   APP_WORKER_POOL_SIZE=50
   APP_QUEUE_CAPACITY=1000
   APP_SCHEDULE_INTERVAL=5m
   ```

2. **定期检查统计日志**
   ```bash
   tail -f logs/api.log | grep "queue statistics"
   ```

3. **设置告警**
   - `total_dropped > 100`: 增加容量
   - `total_failed / total_processed > 0.1`: 检查爬虫服务
- `pending / capacity > 0.9`: 增加 Worker

4. **压力测试**
   ```bash
   # 创建 200 个测试任务
   for i in {1..200}; do
       curl -X POST http://localhost:8080/api/tasks \
           -d '{"keyword":"test'$i'","platform":1}'
   done
   
   # 观察队列统计
   tail -f logs/api.log | grep "queue statistics"
   ```

## 总结

Worker Pool 集成带来了：
- **资源可控**: 固定 goroutine 数量
- **自动排队**: 任务自动等待 Worker 空闲
- **并发限制**: 保护外部 API 和系统资源
- **优雅关闭**: 安全停止服务
- **可观测性**: 完整的统计指标
- **可配置**: 灵活调整参数

现在你的系统可以安全地处理成百上千个任务，而不会出现资源耗尽的问题！
