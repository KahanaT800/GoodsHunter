package queue

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

// Job 表示一个可执行的异步任务。
type Job func(ctx context.Context) error

// ErrorHandler 错误处理回调函数。
type ErrorHandler func(err error, job Job)

// Queue 提供生产级别的内存任务队列与固定 worker 池。
type Queue struct {
	logger       *slog.Logger
	workers      int
	jobs         chan Job
	errorHandler ErrorHandler

	// 优雅关闭
	wg     sync.WaitGroup
	closed atomic.Bool

	// 指标统计
	stats queueStats
}

// queueStats 队列内部统计信息（使用 atomic 类型）。
type queueStats struct {
	TotalEnqueued  atomic.Int64 // 总入队任务数
	TotalProcessed atomic.Int64 // 总处理完成数
	TotalSucceeded atomic.Int64 // 成功任务数
	TotalFailed    atomic.Int64 // 失败任务数
	TotalDropped   atomic.Int64 // 丢弃任务数（队列满）
	TotalPanics    atomic.Int64 // Panic 次数
}

// QueueStats 队列统计信息快照（普通值类型，可安全拷贝）。
type QueueStats struct {
	TotalEnqueued  int64 // 总入队任务数
	TotalProcessed int64 // 总处理完成数
	TotalSucceeded int64 // 成功任务数
	TotalFailed    int64 // 失败任务数
	TotalDropped   int64 // 丢弃任务数（队列满）
	TotalPanics    int64 // Panic 次数
}

// NewQueue 创建一个新的任务队列。
//
// 参数:
//   - logger: 日志记录器
//   - workers: worker 数量（至少为 1）
//   - capacity: 队列容量（至少为 1）
//
// 返回值:
//   - *Queue: 队列实例
func NewQueue(logger *slog.Logger, workers int, capacity int) *Queue {
	if workers <= 0 {
		workers = 1
	}
	if capacity <= 0 {
		capacity = 1
	}
	return &Queue{
		logger:  logger,
		workers: workers,
		jobs:    make(chan Job, capacity),
	}
}

// SetErrorHandler 设置错误处理回调函数。
func (q *Queue) SetErrorHandler(handler ErrorHandler) {
	q.errorHandler = handler
}

// Start 启动 worker 池，直到 ctx 被取消或调用 Shutdown。
func (q *Queue) Start(ctx context.Context) {
	for i := 0; i < q.workers; i++ {
		q.wg.Add(1)
		go q.worker(ctx, i)
	}
}

// worker 单个 worker 的执行逻辑。
func (q *Queue) worker(ctx context.Context, id int) {
	defer q.wg.Done()

	for {
		select {
		case <-ctx.Done():
			q.logger.Debug("worker stopped", slog.Int("worker_id", id))
			return

		case job, ok := <-q.jobs:
			if !ok {
				q.logger.Debug("worker exit on closed channel", slog.Int("worker_id", id))
				return
			}
			if job != nil {
				q.executeJob(ctx, job, id)
			}
		}
	}
}

// executeJob 执行单个任务，带 panic 恢复和错误处理。
func (q *Queue) executeJob(ctx context.Context, job Job, workerID int) {
	defer func() {
		if r := recover(); r != nil {
			q.stats.TotalPanics.Add(1)
			q.logger.Error("job panic recovered",
				slog.Int("worker_id", workerID),
				slog.Any("panic", r),
				slog.String("stack", string(debug.Stack())))
		}
	}()

	err := job(ctx)
	q.stats.TotalProcessed.Add(1)

	if err != nil {
		q.stats.TotalFailed.Add(1)
		q.logger.Warn("job failed",
			slog.Int("worker_id", workerID),
			slog.String("error", err.Error()))

		if q.errorHandler != nil {
			q.errorHandler(err, job)
		}
	} else {
		q.stats.TotalSucceeded.Add(1)
	}
}

// Enqueue 将任务放入队列，若队列已满则返回 false（非阻塞）。
func (q *Queue) Enqueue(job Job) bool {
	if job == nil {
		return false
	}

	if q.closed.Load() {
		q.logger.Warn("queue is closed, reject job")
		return false
	}

	select {
	case q.jobs <- job:
		q.stats.TotalEnqueued.Add(1)
		return true
	default:
		q.stats.TotalDropped.Add(1)
		q.logger.Warn("queue full, drop job",
			slog.Int("capacity", cap(q.jobs)),
			slog.Int("pending", len(q.jobs)))
		return false
	}
}

// EnqueueBlocking 阻塞式入队，直到成功或 ctx 被取消。
func (q *Queue) EnqueueBlocking(ctx context.Context, job Job) error {
	if job == nil {
		return fmt.Errorf("job is nil")
	}

	if q.closed.Load() {
		return fmt.Errorf("queue is closed")
	}

	select {
	case q.jobs <- job:
		q.stats.TotalEnqueued.Add(1)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// EnqueueWithTimeout 带超时的入队，若超时则返回错误。
func (q *Queue) EnqueueWithTimeout(job Job, timeout time.Duration) error {
	if job == nil {
		return fmt.Errorf("job is nil")
	}

	if q.closed.Load() {
		return fmt.Errorf("queue is closed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	return q.EnqueueBlocking(ctx, job)
}

// Shutdown 优雅关闭队列：
//  1. 标记为已关闭（拒绝新任务）
//  2. 关闭任务通道
//  3. 等待所有 worker 完成当前任务
func (q *Queue) Shutdown() {
	if q.closed.CompareAndSwap(false, true) {
		close(q.jobs)
		q.logger.Info("queue shutdown initiated, waiting for workers to finish")
		q.wg.Wait()
		q.logger.Info("queue shutdown completed")
	}
}

// ShutdownWithTimeout 带超时的优雅关闭。
func (q *Queue) ShutdownWithTimeout(timeout time.Duration) error {
	if !q.closed.CompareAndSwap(false, true) {
		return fmt.Errorf("queue already closed")
	}

	close(q.jobs)
	q.logger.Info("queue shutdown initiated with timeout",
		slog.String("timeout", timeout.String()))

	done := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		q.logger.Info("queue shutdown completed")
		return nil
	case <-time.After(timeout):
		q.logger.Error("queue shutdown timeout")
		return fmt.Errorf("shutdown timeout after %s", timeout)
	}
}

// Stats 获取队列统计信息的快照。
func (q *Queue) Stats() QueueStats {
	return QueueStats{
		TotalEnqueued:  q.stats.TotalEnqueued.Load(),
		TotalProcessed: q.stats.TotalProcessed.Load(),
		TotalSucceeded: q.stats.TotalSucceeded.Load(),
		TotalFailed:    q.stats.TotalFailed.Load(),
		TotalDropped:   q.stats.TotalDropped.Load(),
		TotalPanics:    q.stats.TotalPanics.Load(),
	}
}

// Len 返回当前队列中待处理的任务数量。
func (q *Queue) Len() int {
	return len(q.jobs)
}

// Cap 返回队列的容量。
func (q *Queue) Cap() int {
	return cap(q.jobs)
}

// IsClosed 返回队列是否已关闭。
func (q *Queue) IsClosed() bool {
	return q.closed.Load()
}

// String 返回队列的状态描述。
func (q *Queue) String() string {
	stats := q.Stats()
	return fmt.Sprintf("Queue[workers=%d, capacity=%d, pending=%d, closed=%v, enqueued=%d, processed=%d, succeeded=%d, failed=%d, dropped=%d, panics=%d]",
		q.workers,
		q.Cap(),
		q.Len(),
		q.IsClosed(),
		stats.TotalEnqueued,
		stats.TotalProcessed,
		stats.TotalSucceeded,
		stats.TotalFailed,
		stats.TotalDropped,
		stats.TotalPanics,
	)
}
