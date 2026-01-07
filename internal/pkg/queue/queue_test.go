package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestQueue_BasicFunctionality(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	q := NewQueue(logger, 3, 10)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q.Start(ctx)

	var completed atomic.Int32
	for i := 0; i < 5; i++ {
		idx := i
		job := func(ctx context.Context) error {
			t.Logf("Processing job %d", idx)
			time.Sleep(100 * time.Millisecond)
			completed.Add(1)
			return nil
		}
		if !q.Enqueue(job) {
			t.Errorf("Failed to enqueue job %d", i)
		}
	}

	// 等待任务完成
	time.Sleep(1 * time.Second)
	q.Shutdown()

	if completed.Load() != 5 {
		t.Errorf("Expected 5 completed jobs, got %d", completed.Load())
	}

	stats := q.Stats()
	t.Logf("Stats: %s", q.String())
	if stats.TotalEnqueued != 5 {
		t.Errorf("Expected 5 enqueued, got %d", stats.TotalEnqueued)
	}
}

func TestQueue_ErrorHandling(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	q := NewQueue(logger, 2, 5)

	var errorCount atomic.Int32
	q.SetErrorHandler(func(err error, job Job) {
		errorCount.Add(1)
		t.Logf("Error handler called: %v", err)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q.Start(ctx)

	// 成功任务
	q.Enqueue(func(ctx context.Context) error {
		return nil
	})

	// 失败任务
	q.Enqueue(func(ctx context.Context) error {
		return errors.New("task failed")
	})

	time.Sleep(500 * time.Millisecond)
	q.Shutdown()

	stats := q.Stats()
	if stats.TotalSucceeded != 1 {
		t.Errorf("Expected 1 success, got %d", stats.TotalSucceeded)
	}
	if stats.TotalFailed != 1 {
		t.Errorf("Expected 1 failure, got %d", stats.TotalFailed)
	}
	if errorCount.Load() != 1 {
		t.Errorf("Expected 1 error callback, got %d", errorCount.Load())
	}
}

func TestQueue_PanicRecovery(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	q := NewQueue(logger, 2, 5)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q.Start(ctx)

	// Panic 任务
	q.Enqueue(func(ctx context.Context) error {
		panic("intentional panic")
	})

	// 正常任务（验证 worker 没有因为 panic 而挂掉）
	var executed atomic.Bool
	q.Enqueue(func(ctx context.Context) error {
		executed.Store(true)
		return nil
	})

	time.Sleep(500 * time.Millisecond)
	q.Shutdown()

	stats := q.Stats()
	if stats.TotalPanics != 1 {
		t.Errorf("Expected 1 panic, got %d", stats.TotalPanics)
	}
	if !executed.Load() {
		t.Error("Normal job should execute after panic")
	}
}

func TestQueue_QueueFull(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	q := NewQueue(logger, 1, 2) // 1个worker，2个容量

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q.Start(ctx)

	// 填满队列
	blockChan := make(chan struct{})

	// 第1个任务：在 worker 中执行，阻塞住
	q.Enqueue(func(ctx context.Context) error {
		<-blockChan
		return nil
	})

	time.Sleep(50 * time.Millisecond) // 确保第一个任务开始执行

	// 第2、3个任务：填满队列容量（2个slot）
	q.Enqueue(func(ctx context.Context) error { return nil })
	q.Enqueue(func(ctx context.Context) error { return nil })

	// 第4个任务：应该被丢弃（worker忙碌 + 队列满）
	dropped := !q.Enqueue(func(ctx context.Context) error { return nil })
	if !dropped {
		t.Error("Expected enqueue to fail when queue is full")
	}

	close(blockChan) // 释放阻塞
	time.Sleep(300 * time.Millisecond)
	q.Shutdown()

	stats := q.Stats()
	if stats.TotalDropped < 1 {
		t.Errorf("Expected at least 1 dropped job, got %d", stats.TotalDropped)
	}
}

func TestQueue_BlockingEnqueue(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	q := NewQueue(logger, 1, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q.Start(ctx)

	// 阻塞队列
	blockChan := make(chan struct{})
	q.Enqueue(func(ctx context.Context) error {
		<-blockChan
		return nil
	})

	time.Sleep(50 * time.Millisecond) // 确保第一个任务开始执行

	// 再填满队列容量
	q.Enqueue(func(ctx context.Context) error {
		return nil
	})

	// 测试超时的阻塞入队（队列已满，应该超时）
	timeoutCtx, timeoutCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer timeoutCancel()

	start := time.Now()
	err := q.EnqueueBlocking(timeoutCtx, func(ctx context.Context) error {
		return nil
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Error("Expected timeout error")
	}

	if elapsed < 80*time.Millisecond {
		t.Errorf("Expected to wait ~100ms, but only waited %v", elapsed)
	}

	close(blockChan)
	time.Sleep(100 * time.Millisecond)
	q.Shutdown()
}

func TestQueue_EnqueueWithTimeout(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	q := NewQueue(logger, 10, 100)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q.Start(ctx)

	err := q.EnqueueWithTimeout(func(ctx context.Context) error {
		return nil
	}, 1*time.Second)

	if err != nil {
		t.Errorf("Expected successful enqueue, got error: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	q.Shutdown()
}

func TestQueue_GracefulShutdown(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	q := NewQueue(logger, 3, 10)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q.Start(ctx)

	var completed atomic.Int32
	for i := 0; i < 10; i++ {
		q.Enqueue(func(ctx context.Context) error {
			time.Sleep(100 * time.Millisecond)
			completed.Add(1)
			return nil
		})
	}

	// 优雅关闭，等待所有任务完成
	q.Shutdown()

	if completed.Load() != 10 {
		t.Errorf("Expected all 10 jobs to complete, got %d", completed.Load())
	}

	// 关闭后不应接受新任务
	if q.Enqueue(func(ctx context.Context) error { return nil }) {
		t.Error("Should not accept jobs after shutdown")
	}
}

func TestQueue_ShutdownWithTimeout(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	q := NewQueue(logger, 2, 5)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q.Start(ctx)

	// 添加一些快速任务
	for i := 0; i < 3; i++ {
		q.Enqueue(func(ctx context.Context) error {
			time.Sleep(50 * time.Millisecond)
			return nil
		})
	}

	// 500ms 足够完成所有任务
	err := q.ShutdownWithTimeout(500 * time.Millisecond)
	if err != nil {
		t.Errorf("Expected successful shutdown, got error: %v", err)
	}
}

func TestQueue_Stats(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	q := NewQueue(logger, 2, 5)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q.Start(ctx)

	// 成功任务
	q.Enqueue(func(ctx context.Context) error { return nil })
	q.Enqueue(func(ctx context.Context) error { return nil })

	// 失败任务
	q.Enqueue(func(ctx context.Context) error { return errors.New("error") })

	time.Sleep(500 * time.Millisecond)
	q.Shutdown()

	stats := q.Stats()
	t.Logf("Final stats: %s", q.String())

	if stats.TotalEnqueued != 3 {
		t.Errorf("Expected 3 enqueued, got %d", stats.TotalEnqueued)
	}
	if stats.TotalProcessed != 3 {
		t.Errorf("Expected 3 processed, got %d", stats.TotalProcessed)
	}
	if stats.TotalSucceeded != 2 {
		t.Errorf("Expected 2 succeeded, got %d", stats.TotalSucceeded)
	}
	if stats.TotalFailed != 1 {
		t.Errorf("Expected 1 failed, got %d", stats.TotalFailed)
	}
}

func BenchmarkQueue_Enqueue(b *testing.B) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	q := NewQueue(logger, 10, 1000)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q.Start(ctx)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q.Enqueue(func(ctx context.Context) error {
			return nil
		})
	}

	q.Shutdown()
}

// ExampleQueue 演示基本用法。
func ExampleQueue() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// 创建队列：3个worker，容量10
	q := NewQueue(logger, 3, 10)

	// 设置错误处理器
	q.SetErrorHandler(func(err error, job Job) {
		fmt.Printf("Job failed: %v\n", err)
	})

	ctx := context.Background()
	q.Start(ctx)

	// 提交任务
	for i := 0; i < 5; i++ {
		idx := i
		q.Enqueue(func(ctx context.Context) error {
			fmt.Printf("Processing job %d\n", idx)
			time.Sleep(100 * time.Millisecond)
			return nil
		})
	}

	// 优雅关闭
	q.Shutdown()

	// 打印统计
	fmt.Println(q.String())
}
