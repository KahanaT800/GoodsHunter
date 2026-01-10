# 爬虫服务 Worker Pool 集成

## 概述

爬虫服务已成功集成 Worker Pool，替换了原有的信号量（`chan struct{}`）并发控制机制。

## 主要变更

### 1. Service 结构体

**之前**：
```go
type Service struct {
    browser *rod.Browser
    sem     chan struct{} // 信号量控制并发
    logger  *slog.Logger
    // ...
}
```

**之后**：
```go
type Service struct {
    browser *rod.Browser
    queue   *queue.Queue // Worker Pool 控制并发
    logger  *slog.Logger
    // ...
}
```

### 2. 并发控制机制

**之前 - 信号量模式**：
```go
// 获取信号量
s.sem <- struct{}{}
defer func() { <-s.sem }()

// 执行爬取...
```

**之后 - Worker Pool 模式**：
```go
// 创建任务并提交到 Worker Pool
job := func(workerCtx context.Context) error {
    response, err := s.executeCrawl(ctx, req)
    // 处理结果...
    return err
}

// 阻塞式提交（等待队列有空间）
s.queue.EnqueueBlocking(ctx, job)
```

### 3. 优雅关闭

**新增功能**：
- 服务启动时自动启动 Worker Pool
- 支持优雅关闭，等待所有任务完成
- 30 秒超时保护

```go
// 优雅关闭
shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

if err := service.Shutdown(shutdownCtx); err != nil {
    logger.Error("worker pool shutdown error")
}
```

## 配置说明

Worker Pool 的大小由配置文件中的浏览器并发数决定：

```json
{
  "browser": {
    "max_concurrency": 10  // Worker 数量 = 10
  }
}
```

- **Workers 数量**：`max_concurrency`
- **队列容量**：`max_concurrency × 2`（避免请求积压）

## 统计信息

可以通过 `Stats()` 方法获取 Worker Pool 的实时统计：

```go
stats := service.Stats()
fmt.Printf("Total Enqueued: %d\n", stats.TotalEnqueued)
fmt.Printf("Total Processed: %d\n", stats.TotalProcessed)
fmt.Printf("Total Succeeded: %d\n", stats.TotalSucceeded)
fmt.Printf("Total Failed: %d\n", stats.TotalFailed)
```

## 优势

### 相比信号量的改进

1. **更好的任务管理**
   - 统一的任务队列
   - 自动重试机制（可选）
   - 丰富的统计信息

2. **优雅关闭**
   - 等待所有任务完成
   - 超时保护机制
   - 资源清理保证

3. **错误处理**
   - Panic 自动恢复
   - 错误回调处理器
   - 详细的错误日志

4. **可观测性**
   - 实时统计信息
   - 任务执行状态
   - 性能指标

5. **统一架构**
   - 与 API Scheduler 使用相同的 Worker Pool
   - 代码复用
   - 维护成本降低

## 使用示例

### 启动服务

```go
service, err := crawler.NewService(ctx, cfg, logger)
if err != nil {
    log.Fatal(err)
}
// Worker Pool 已自动启动
```

### 提交爬取任务

```go
response, err := service.FetchItems(ctx, &pb.FetchRequest{
    TaskId:   "task-123",
    Platform: pb.Platform_MERCARI,
    Keyword:  "Nintendo Switch",
})
```

### 关闭服务

```go
shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

if err := service.Shutdown(shutdownCtx); err != nil {
    logger.Error("shutdown error", slog.String("error", err.Error()))
}
```

## 性能考虑

### 最佳实践

1. **合理设置并发数**
   - 浏览器资源消耗较大
   - 建议：2-10 个并发（取决于硬件）
   - 避免设置过大导致 OOM

2. **队列容量**
   - 自动设置为 `Workers × 2`
   - 平衡内存使用和吞吐量

3. **超时设置**
   - 页面加载超时：15 秒
   - 关闭超时：30 秒

## 监控建议

定期记录统计信息：

```go
ticker := time.NewTicker(1 * time.Minute)
go func() {
    for range ticker.C {
        stats := service.Stats()
        logger.Info("crawler stats",
            slog.Int64("enqueued", stats.TotalEnqueued),
            slog.Int64("processed", stats.TotalProcessed),
            slog.Int64("succeeded", stats.TotalSucceeded),
            slog.Int64("failed", stats.TotalFailed),
        )
    }
}()
```

## 相关文档

- [Worker Pool 实现](../internal/pkg/queue/README.md)
- [Worker Pool 集成到 Scheduler](WORKER_POOL_INTEGRATION.md)
- [配置指南](CONFIG_GUIDE.md)
