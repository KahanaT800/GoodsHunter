# ADR-0004: Worker 自杀策略

## 状态

已采纳

## 背景

GoodsHunter Crawler Worker 使用 Headless Chrome (go-rod) 进行页面渲染。长时间运行后观察到以下问题：

1. **内存泄漏**: Chrome 进程内存持续增长
2. **僵尸进程**: 偶发的页面 Crash 导致孤儿进程
3. **状态污染**: 累积的 Cookie/Storage 可能触发反爬
4. **性能下降**: 长时间运行后响应变慢

## 决策

采用 **"Suicide on Quota"** 策略：Worker 处理固定数量任务后主动退出，由 Docker 自动重启。

### 配置

| 参数 | 环境变量 | 默认值 | 说明 |
|------|----------|--------|------|
| maxTasks | `MAX_TASKS` | 500 | 最大处理任务数 |

### 工作流程

```
1. Worker 启动 (干净状态)
2. 处理任务，计数器 +1
3. 检查: counter >= maxTasks?
   - 否: 继续处理
   - 是: 发送重启信号
4. 主进程收到信号，优雅关闭
5. Docker restart: always 自动重启
6. 回到步骤 1
```

### 优雅关闭

```go
select {
case sig := <-sigCh:
    // 外部信号 (SIGTERM/SIGINT)
case <-restartCh:
    // 内部重启信号 (maxTasks 达到)
}

// 停止接收新任务
stopWorkers()

// 等待当前任务完成 (最长 30s)
service.Shutdown(ctx)
```

## 考虑的选项

### 选项 1: 不限制，持续运行

**优点:**
- 无重启开销
- 实现简单

**缺点:**
- 内存泄漏累积
- 长期稳定性差
- 难以预测的崩溃

### 选项 2: 定期重启 (基于时间)

**优点:**
- 简单可预测
- 可以选择低峰期重启

**缺点:**
- 与实际负载无关
- 可能在处理任务时中断
- 不同负载下效果不一

### 选项 3: 内存阈值重启

**优点:**
- 直接解决内存问题
- 按需触发

**缺点:**
- 内存监控有开销
- 阈值难以确定
- 可能已经 OOM

### 选项 4: Suicide on Quota (已采纳)

**优点:**
- 可预测的生命周期
- 与负载成正比
- 保证干净状态
- 简单可靠

**缺点:**
- 重启有短暂中断
- 需要 Docker 配合

## 后果

### 正面

- **内存稳定**: 定期释放所有资源，内存保持稳定
- **状态清洁**: 每次重启都是全新状态
- **可预测**: 已知每 500 个任务重启一次
- **自愈能力**: 任何累积的问题都会在重启时清除

### 负面

- **重启开销**: 浏览器冷启动需要 5-10 秒
- **任务延迟**: 重启期间队列中的任务需要等待
- **Docker 依赖**: 需要 `restart: always` 配置

## 容量规划

| 场景 | 每日任务 | 重启次数 | 不可用时间 |
|------|----------|----------|------------|
| 低负载 | 1,000 | 2 次 | ~20 秒 |
| 中等负载 | 5,000 | 10 次 | ~100 秒 |
| 高负载 | 20,000 | 40 次 | ~400 秒 |

对于高负载场景，建议部署多个 Worker 以覆盖重启期间的处理能力。

## Docker 配置

```yaml
services:
  crawler:
    restart: always
    environment:
      - MAX_TASKS=500
```

## 实现位置

- `internal/crawler/service.go` (taskCounter, maxTasks, restartCh)
- `cmd/crawler/main.go` (RestartSignal 处理)

## 监控

建议监控：
- `crawler_tasks_processed_total`: 累计处理任务数
- 重启事件日志: "restart requested by service (max tasks reached)"
