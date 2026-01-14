package redisqueue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"goodshunter/internal/pkg/metrics"
	"goodshunter/proto/pb"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	KeyTaskQueue           = "goodshunter:queue:tasks"
	KeyTaskProcessingQueue = "goodshunter:queue:tasks:processing"
	KeyResultQueue         = "goodshunter:queue:results"
	KeyTaskPendingSet      = "goodshunter:queue:tasks:pending" // 去重集合
)

var (
	ErrNoTask     = errors.New("no task available")
	ErrNoResult   = errors.New("no result available")
	ErrTaskExists = errors.New("task already in queue") // 任务已存在
)

// Client wraps Redis List operations for task/result queues.
type Client struct {
	rdb *redis.Client
}

// NewClient creates a redisqueue client with address/password.
func NewClient(addr, password string) *Client {
	return &Client{
		rdb: redis.NewClient(&redis.Options{
			Addr:     addr,
			Password: password,
			DB:       0,
		}),
	}
}

// NewClientWithRedis creates a redisqueue client from an existing redis.Client.
func NewClientWithRedis(rdb *redis.Client) (*Client, error) {
	if rdb == nil {
		return nil, errors.New("redis client is nil")
	}
	return &Client{rdb: rdb}, nil
}

// PushTask serializes a FetchRequest and pushes it into the task queue.
// 使用 Redis Set 进行去重：如果任务已在队列中，返回 ErrTaskExists。
func (c *Client) PushTask(ctx context.Context, task *pb.FetchRequest) error {
	if task == nil {
		return errors.New("task is nil")
	}
	if c == nil || c.rdb == nil {
		return errors.New("redis client is not initialized")
	}

	taskID := task.GetTaskId()
	if taskID == "" {
		return errors.New("task id is empty")
	}

	// 使用 SADD 尝试添加到去重集合
	// 如果返回 0，说明已存在，跳过
	added, err := c.rdb.SAdd(ctx, KeyTaskPendingSet, taskID).Result()
	if err != nil {
		return fmt.Errorf("sadd pending set: %w", err)
	}
	if added == 0 {
		// 任务已在队列中，跳过
		metrics.CrawlerTaskThroughput.WithLabelValues("in", "skipped").Inc()
		metrics.SchedulerTasksSkippedTotal.Inc()
		return ErrTaskExists
	}

	// 序列化并推送到队列
	data, err := protojson.Marshal(task)
	if err != nil {
		// 推送失败，从集合中移除
		c.rdb.SRem(ctx, KeyTaskPendingSet, taskID)
		return fmt.Errorf("marshal task: %w", err)
	}
	if err := c.rdb.LPush(ctx, KeyTaskQueue, string(data)).Err(); err != nil {
		// 推送失败，从集合中移除
		c.rdb.SRem(ctx, KeyTaskPendingSet, taskID)
		return fmt.Errorf("lpush task: %w", err)
	}

	metrics.CrawlerTaskThroughput.WithLabelValues("in", "pushed").Inc()
	metrics.SchedulerTasksPushedTotal.Inc()
	return nil
}

// PushTaskForce 强制推送任务，不检查去重（用于特殊场景）。
func (c *Client) PushTaskForce(ctx context.Context, task *pb.FetchRequest) error {
	if task == nil {
		return errors.New("task is nil")
	}
	if c == nil || c.rdb == nil {
		return errors.New("redis client is not initialized")
	}
	data, err := protojson.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal task: %w", err)
	}
	if err := c.rdb.LPush(ctx, KeyTaskQueue, string(data)).Err(); err != nil {
		return fmt.Errorf("lpush task: %w", err)
	}
	// 也加入 pending set
	if taskID := task.GetTaskId(); taskID != "" {
		c.rdb.SAdd(ctx, KeyTaskPendingSet, taskID)
	}
	metrics.CrawlerTaskThroughput.WithLabelValues("in", "pushed").Inc()
	return nil
}

// PopTask blocks until a task is available or timeout is reached.
func (c *Client) PopTask(ctx context.Context, timeout time.Duration) (*pb.FetchRequest, error) {
	if c == nil || c.rdb == nil {
		return nil, errors.New("redis client is not initialized")
	}
	result, err := c.rdb.BRPopLPush(ctx, KeyTaskQueue, KeyTaskProcessingQueue, timeout).Result()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNoTask
	}
	if err != nil {
		return nil, fmt.Errorf("brpoplpush task: %w", err)
	}

	var req pb.FetchRequest
	if err := protojson.Unmarshal([]byte(result), &req); err != nil {
		return nil, fmt.Errorf("unmarshal task: %w", err)
	}
	metrics.CrawlerTaskThroughput.WithLabelValues("out", "popped").Inc()
	return &req, nil
}

// PushResult serializes a FetchResponse and pushes it into the result queue.
func (c *Client) PushResult(ctx context.Context, res *pb.FetchResponse) error {
	if res == nil {
		return errors.New("result is nil")
	}
	if c == nil || c.rdb == nil {
		return errors.New("redis client is not initialized")
	}
	data, err := protojson.Marshal(res)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}
	if err := c.rdb.LPush(ctx, KeyResultQueue, string(data)).Err(); err != nil {
		return fmt.Errorf("lpush result: %w", err)
	}
	return nil
}

// PopResult blocks until a result is available or timeout is reached.
func (c *Client) PopResult(ctx context.Context, timeout time.Duration) (*pb.FetchResponse, error) {
	if c == nil || c.rdb == nil {
		return nil, errors.New("redis client is not initialized")
	}
	result, err := c.rdb.BRPop(ctx, timeout, KeyResultQueue).Result()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNoResult
	}
	if err != nil {
		return nil, fmt.Errorf("brpop result: %w", err)
	}
	if len(result) < 2 {
		return nil, fmt.Errorf("invalid brpop response: %v", result)
	}

	var resp pb.FetchResponse
	if err := protojson.Unmarshal([]byte(result[1]), &resp); err != nil {
		return nil, fmt.Errorf("unmarshal result: %w", err)
	}
	return &resp, nil
}

// AckTask removes a processed task from the processing queue and pending set.
// 这允许该任务在下一个调度周期被重新推送。
func (c *Client) AckTask(ctx context.Context, task *pb.FetchRequest) error {
	if task == nil {
		return errors.New("task is nil")
	}
	if c == nil || c.rdb == nil {
		return errors.New("redis client is not initialized")
	}
	data, err := protojson.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal task: %w", err)
	}

	// 使用 Pipeline 原子执行：从处理队列和 pending set 中移除
	pipe := c.rdb.TxPipeline()
	pipe.LRem(ctx, KeyTaskProcessingQueue, 1, string(data))
	if taskID := task.GetTaskId(); taskID != "" {
		pipe.SRem(ctx, KeyTaskPendingSet, taskID)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("ack task failed: %w", err)
	}
	return nil
}

// QueueDepth returns the current length of task and result queues.
func (c *Client) QueueDepth(ctx context.Context) (int64, int64, error) {
	if c == nil || c.rdb == nil {
		return 0, 0, errors.New("redis client is not initialized")
	}
	tasks, err := c.rdb.LLen(ctx, KeyTaskQueue).Result()
	if err != nil {
		return 0, 0, fmt.Errorf("llen tasks: %w", err)
	}
	results, err := c.rdb.LLen(ctx, KeyResultQueue).Result()
	if err != nil {
		return 0, 0, fmt.Errorf("llen results: %w", err)
	}
	return tasks, results, nil
}

// PendingSetSize returns the number of unique tasks currently pending.
func (c *Client) PendingSetSize(ctx context.Context) (int64, error) {
	if c == nil || c.rdb == nil {
		return 0, errors.New("redis client is not initialized")
	}
	size, err := c.rdb.SCard(ctx, KeyTaskPendingSet).Result()
	if err != nil {
		return 0, fmt.Errorf("scard pending set: %w", err)
	}
	return size, nil
}

// RescueStuckTasks scans processing queue and requeues tasks that exceed timeout.
func (c *Client) RescueStuckTasks(ctx context.Context, timeout time.Duration) (int, error) {
	if c == nil || c.rdb == nil {
		return 0, errors.New("redis client is not initialized")
	}
	tasksRaw, err := c.rdb.LRange(ctx, KeyTaskProcessingQueue, 0, -1).Result()
	if err != nil {
		return 0, fmt.Errorf("lrange processing: %w", err)
	}
	if len(tasksRaw) == 0 {
		return 0, nil
	}

	now := time.Now().Unix()
	threshold := int64(timeout.Seconds())
	rescued := 0

	for _, raw := range tasksRaw {
		var task pb.FetchRequest
		if err := protojson.Unmarshal([]byte(raw), &task); err != nil {
			continue
		}
		if task.GetCreatedAt() == 0 {
			continue
		}
		if now-task.GetCreatedAt() <= threshold {
			continue
		}

		pipe := c.rdb.TxPipeline()
		pipe.LRem(ctx, KeyTaskProcessingQueue, 1, raw)
		pipe.LPush(ctx, KeyTaskQueue, raw)
		if _, execErr := pipe.Exec(ctx); execErr == nil {
			rescued++
		}
	}

	return rescued, nil
}
