package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"goodshunter/internal/pkg/queue"
	"goodshunter/internal/pkg/taskqueue"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestHandleTaskMessage_SuccessAck(t *testing.T) {
	ctx := context.Background()
	rdb, cleanup := newMiniRedis(t)
	defer cleanup()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	consumer, err := taskqueue.NewConsumer(rdb, logger, "goodshunter:task:queue", "test_group", "c1")
	if err != nil {
		t.Fatalf("new consumer: %v", err)
	}

	msg := taskqueue.NewExecuteMessage(1, "test")
	msgID := addStreamMessage(t, rdb, msg)
	read := readOneMessage(t, consumer, ctx)

	s := &Scheduler{
		logger:       logger,
		queue:        queue.NewQueue(logger, 1, 10),
		taskConsumer: consumer,
		taskHandler: func(ctx context.Context, taskID uint) error {
			return nil
		},
	}
	s.queue.Start(ctx)

	s.handleTaskMessage(ctx, read)

	waitForPendingCount(t, rdb, "goodshunter:task:queue", "test_group", 0)
	if read.ID != msgID {
		t.Fatalf("expected msgID %s, got %s", msgID, read.ID)
	}
}

func TestHandleTaskMessage_Retry(t *testing.T) {
	ctx := context.Background()
	rdb, cleanup := newMiniRedis(t)
	defer cleanup()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	consumer, err := taskqueue.NewConsumer(
		rdb,
		logger,
		"goodshunter:task:queue",
		"test_group",
		"c1",
		taskqueue.WithMaxRetry(2),
	)
	if err != nil {
		t.Fatalf("new consumer: %v", err)
	}

	msg := taskqueue.NewExecuteMessage(2, "test")
	addStreamMessage(t, rdb, msg)
	read := readOneMessage(t, consumer, ctx)

	s := &Scheduler{
		logger:       logger,
		queue:        queue.NewQueue(logger, 1, 10),
		taskConsumer: consumer,
		taskHandler: func(ctx context.Context, taskID uint) error {
			return errors.New("boom")
		},
	}
	s.queue.Start(ctx)

	s.handleTaskMessage(ctx, read)

	waitForPendingCount(t, rdb, "goodshunter:task:queue", "test_group", 0)

	last := lastStreamMessage(t, rdb, "goodshunter:task:queue")
	if last == "" {
		t.Fatalf("expected retry message")
	}
	var parsed taskqueue.TaskMessage
	if err := json.Unmarshal([]byte(last), &parsed); err != nil {
		t.Fatalf("unmarshal retry msg: %v", err)
	}
	if parsed.Retry != 1 {
		t.Fatalf("expected retry=1, got %d", parsed.Retry)
	}
}

func TestHandleTaskMessage_DLQ(t *testing.T) {
	ctx := context.Background()
	rdb, cleanup := newMiniRedis(t)
	defer cleanup()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	consumer, err := taskqueue.NewConsumer(
		rdb,
		logger,
		"goodshunter:task:queue",
		"test_group",
		"c1",
		taskqueue.WithMaxRetry(0),
	)
	if err != nil {
		t.Fatalf("new consumer: %v", err)
	}

	msg := taskqueue.NewExecuteMessage(3, "test")
	addStreamMessage(t, rdb, msg)
	read := readOneMessage(t, consumer, ctx)

	s := &Scheduler{
		logger:       logger,
		queue:        queue.NewQueue(logger, 1, 10),
		taskConsumer: consumer,
		taskHandler: func(ctx context.Context, taskID uint) error {
			return errors.New("boom")
		},
	}
	s.queue.Start(ctx)

	s.handleTaskMessage(ctx, read)

	waitForPendingCount(t, rdb, "goodshunter:task:queue", "test_group", 0)
	dlqLen := xlen(t, rdb, "goodshunter:task:queue:dlq")
	if dlqLen == 0 {
		t.Fatalf("expected DLQ message")
	}
}

func TestEnqueueTaskMessage_BlocksWhenFull(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := queue.NewQueue(logger, 1, 1)
	if err := q.EnqueueBlocking(context.Background(), func(ctx context.Context) error { return nil }); err != nil {
		t.Fatalf("fill queue: %v", err)
	}

	rdb, cleanup := newMiniRedis(t)
	defer cleanup()

	consumer, err := taskqueue.NewConsumer(rdb, logger, "goodshunter:task:queue", "test_group", "c1")
	if err != nil {
		t.Fatalf("new consumer: %v", err)
	}

	s := &Scheduler{
		logger:       logger,
		queue:        q,
		taskConsumer: consumer,
		taskHandler: func(ctx context.Context, taskID uint) error {
			return nil
		},
	}

	msg := taskqueue.NewExecuteMessage(4, "test")
	addStreamMessage(t, rdb, msg)
	read := readOneMessage(t, consumer, context.Background())

	start := time.Now()
	s.enqueueTaskMessage(ctx, read)
	if time.Since(start) < 45*time.Millisecond {
		t.Fatalf("expected blocking enqueue")
	}
}

func newMiniRedis(t *testing.T) (*redis.Client, func()) {
	t.Helper()
	s, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	return rdb, func() {
		_ = rdb.Close()
		s.Close()
	}
}

func addStreamMessage(t *testing.T, rdb *redis.Client, msg *taskqueue.TaskMessage) string {
	t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal msg: %v", err)
	}
	id, err := rdb.XAdd(context.Background(), &redis.XAddArgs{
		Stream: "goodshunter:task:queue",
		Values: map[string]interface{}{"data": string(data)},
	}).Result()
	if err != nil {
		t.Fatalf("xadd: %v", err)
	}
	return id
}

func readOneMessage(t *testing.T, consumer *taskqueue.Consumer, ctx context.Context) *taskqueue.MessageWithID {
	t.Helper()
	msgs, err := consumer.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatalf("expected message")
	}
	return msgs[0]
}

func waitForPendingCount(t *testing.T, rdb *redis.Client, stream, group string, want int64) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		info, err := rdb.XPending(context.Background(), stream, group).Result()
		if err == nil && info.Count == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pending count not %d", want)
}

func lastStreamMessage(t *testing.T, rdb *redis.Client, stream string) string {
	t.Helper()
	msgs, err := rdb.XRevRangeN(context.Background(), stream, "+", "-", 1).Result()
	if err != nil || len(msgs) == 0 {
		return ""
	}
	val, ok := msgs[0].Values["data"].(string)
	if !ok {
		return ""
	}
	return val
}

func xlen(t *testing.T, rdb *redis.Client, stream string) int64 {
	t.Helper()
	val, err := rdb.XLen(context.Background(), stream).Result()
	if err != nil {
		t.Fatalf("xlen: %v", err)
	}
	return val
}
