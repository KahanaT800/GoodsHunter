# Worker Pool Queue

生产级别的内存任务队列与固定 Worker 池实现。

## 特性

✅ **固定 Worker 池**：指定数量的 goroutine 处理任务，避免无限制创建协程  
✅ **优雅关闭**：支持等待所有任务完成或超时关闭  
✅ **Panic 恢复**：自动捕获任务 panic，不会影响 worker 运行  
✅ **错误处理**：支持自定义错误处理回调  
✅ **统计指标**：实时追踪入队、成功、失败、丢弃、panic 等指标  
✅ **多种入队模式**：非阻塞、阻塞、超时  
✅ **线程安全**：使用 atomic 操作和 channel，无竞态条件  

## 快速开始

### 基本用法

```go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"goodshunter/internal/pkg/queue"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// 创建队列：5个worker，容量100
	q := queue.NewQueue(logger, 5, 100)

	ctx := context.Background()
	q.Start(ctx)

	// 提交任务
	for i := 0; i < 10; i++ {
		idx := i
		q.Enqueue(func(ctx context.Context) error {
			fmt.Printf("Processing task %d\n", idx)
			time.Sleep(100 * time.Millisecond)
			return nil
		})
	}

	// 优雅关闭，等待所有任务完成
	q.Shutdown()

	// 打印统计
	fmt.Println(q.String())
}
```

### 错误处理

```go
// 设置错误处理回调
q.SetErrorHandler(func(err error, job queue.Job) {
	logger.Error("task failed", slog.String("error", err.Error()))
	// 可以记录到数据库、发送告警等
})

// 提交可能失败的任务
q.Enqueue(func(ctx context.Context) error {
	// 处理逻辑
	if someCondition {
		return fmt.Errorf("task failed: %v", reason)
	}
	return nil
})
```

### 阻塞入队

```go
// 方式1：一直等待直到成功或 context 取消
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

err := q.EnqueueBlocking(ctx, func(ctx context.Context) error {
	// 任务逻辑
	return nil
})
if err != nil {
	log.Printf("Enqueue failed: %v", err)
}

// 方式2：使用超时
err = q.EnqueueWithTimeout(func(ctx context.Context) error {
	// 任务逻辑
	return nil
}, 3*time.Second)
```

### 优雅关闭

```go
// 方式1：无限等待所有任务完成
q.Shutdown()

// 方式2：最多等待 10 秒
err := q.ShutdownWithTimeout(10 * time.Second)
if err != nil {
	logger.Error("shutdown timeout", slog.String("error", err.Error()))
}
```

### 监控统计

```go
stats := q.Stats()
fmt.Printf("Enqueued: %d\n", stats.TotalEnqueued.Load())
fmt.Printf("Processed: %d\n", stats.TotalProcessed.Load())
fmt.Printf("Succeeded: %d\n", stats.TotalSucceeded.Load())
fmt.Printf("Failed: %d\n", stats.TotalFailed.Load())
fmt.Printf("Dropped: %d\n", stats.TotalDropped.Load())
fmt.Printf("Panics: %d\n", stats.TotalPanics.Load())

// 或者直接打印
fmt.Println(q.String())
// 输出: Queue[workers=5, capacity=100, pending=3, closed=false, enqueued=100, processed=97, succeeded=95, failed=2, dropped=0, panics=0]
```

## API 文档

### 创建队列

```go
func NewQueue(logger *slog.Logger, workers int, capacity int) *Queue
```

- `logger`: 日志记录器
- `workers`: Worker 数量（至少为 1）
- `capacity`: 队列容量（至少为 1）

### 核心方法

#### Start

启动 worker 池。

```go
func (q *Queue) Start(ctx context.Context)
```

#### Enqueue (非阻塞)

尝试将任务放入队列，若队列满则立即返回 false。

```go
func (q *Queue) Enqueue(job Job) bool
```

#### EnqueueBlocking (阻塞)

阻塞式入队，直到成功或 context 被取消。

```go
func (q *Queue) EnqueueBlocking(ctx context.Context, job Job) error
```

#### EnqueueWithTimeout

带超时的入队。

```go
func (q *Queue) EnqueueWithTimeout(job Job, timeout time.Duration) error
```

#### Shutdown

优雅关闭队列，等待所有任务完成。

```go
func (q *Queue) Shutdown()
```

#### ShutdownWithTimeout

带超时的优雅关闭。

```go
func (q *Queue) ShutdownWithTimeout(timeout time.Duration) error
```

### 辅助方法

```go
func (q *Queue) SetErrorHandler(handler ErrorHandler)  // 设置错误处理器
func (q *Queue) Stats() QueueStats                     // 获取统计信息
func (q *Queue) Len() int                              // 当前队列长度
func (q *Queue) Cap() int                              // 队列容量
func (q *Queue) IsClosed() bool                        // 是否已关闭
func (q *Queue) String() string                        // 状态描述
```

## 使用场景

### 1. 限流处理

```go
// 限制同时处理 10 个请求
q := queue.NewQueue(logger, 10, 1000)
q.Start(ctx)

http.HandleFunc("/process", func(w http.ResponseWriter, r *http.Request) {
	if !q.Enqueue(func(ctx context.Context) error {
		// 处理请求
		return processRequest(r)
	}) {
		http.Error(w, "Server busy", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusAccepted)
})
```

### 2. 异步任务处理

```go
// 爬虫任务队列
q := queue.NewQueue(logger, 50, 10000)
q.Start(ctx)

for _, task := range tasks {
	t := task
	q.Enqueue(func(ctx context.Context) error {
		return crawler.FetchAndProcess(t)
	})
}

q.Shutdown() // 等待所有爬取完成
```

### 3. 批量数据处理

```go
// 批量写入数据库
q := queue.NewQueue(logger, 5, 1000)
q.Start(ctx)

q.SetErrorHandler(func(err error, job queue.Job) {
	metrics.IncrementCounter("db_write_failures")
})

for _, record := range records {
	r := record
	q.Enqueue(func(ctx context.Context) error {
		return db.Insert(r)
	})
}

q.ShutdownWithTimeout(30 * time.Second)
```

## 性能建议

1. **Worker 数量**：
   - CPU 密集型：`runtime.NumCPU()`
   - IO 密集型：`runtime.NumCPU() * 2` 或更多
   - 网络请求：根据外部 API 限制调整

2. **队列容量**：
   - 根据内存限制和任务大小设置
   - 建议：`workers * 100` 到 `workers * 1000`

3. **监控指标**：
   - 定期检查 `TotalDropped`，如果持续增长说明容量不足
   - 监控 `TotalFailed` 和 `TotalPanics`，及时发现问题

## 测试

运行所有测试：

```bash
go test -v ./internal/pkg/queue/
```

运行基准测试：

```bash
go test -bench=. ./internal/pkg/queue/
```

## 注意事项

⚠️ 队列关闭后无法重新启动，需要创建新实例  
⚠️ Job 函数应该处理 context 取消信号  
⚠️ 避免在 Job 中执行阻塞操作而不检查 context  
⚠️ 错误处理器不应该阻塞或 panic  
