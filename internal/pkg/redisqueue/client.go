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
	KeyTaskStartedHash     = "goodshunter:queue:tasks:started" // 任务开始处理时间 (task_id -> unix timestamp)
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

// pushTaskScript 原子性地执行 SADD + LPUSH，避免中间状态不一致。
// KEYS[1] = pending set, KEYS[2] = task queue
// ARGV[1] = task_id, ARGV[2] = task JSON
// 返回: 1 = 成功推送, 0 = 任务已存在
var pushTaskScript = redis.NewScript(`
	local added = redis.call('SADD', KEYS[1], ARGV[1])
	if added == 0 then
		return 0
	end
	redis.call('LPUSH', KEYS[2], ARGV[2])
	return 1
`)

// PushTask serializes a FetchRequest and pushes it into the task queue.
// 使用 Lua 脚本原子执行 SADD + LPUSH，确保一致性。
// 如果任务已在队列中，返回 ErrTaskExists。
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

	// 序列化
	data, err := protojson.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal task: %w", err)
	}

	// 使用 Lua 脚本原子执行 SADD + LPUSH
	result, err := pushTaskScript.Run(ctx, c.rdb,
		[]string{KeyTaskPendingSet, KeyTaskQueue},
		taskID, string(data),
	).Int()
	if err != nil {
		return fmt.Errorf("push task script: %w", err)
	}

	if result == 0 {
		// 任务已在队列中，跳过
		metrics.CrawlerTaskThroughput.WithLabelValues("in", "skipped").Inc()
		metrics.SchedulerTasksSkippedTotal.Inc()
		return ErrTaskExists
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
// 同时记录任务开始处理的时间到 KeyTaskStartedHash。
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

	// 记录任务开始处理的时间（用于 Janitor 判断超时）
	if taskID := req.GetTaskId(); taskID != "" {
		c.rdb.HSet(ctx, KeyTaskStartedHash, taskID, time.Now().Unix())
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

// ackTaskScript 原子性地从 processing queue 中找到并删除匹配 task_id 的任务。
// KEYS[1] = processing queue, KEYS[2] = pending set, KEYS[3] = started hash
// ARGV[1] = task_id
// 返回: 删除的任务数量
var ackTaskScript = redis.NewScript(`
	local queue = KEYS[1]
	local pending = KEYS[2]
	local started = KEYS[3]
	local taskId = ARGV[1]
	
	-- 遍历 processing queue 找到匹配的任务
	local tasks = redis.call('LRANGE', queue, 0, -1)
	local removed = 0
	for _, task in ipairs(tasks) do
		-- 检查 JSON 中是否包含该 task_id
		if string.find(task, '"taskId":"' .. taskId .. '"') then
			redis.call('LREM', queue, 1, task)
			removed = removed + 1
			break
		end
	end
	
	-- 从 pending set 和 started hash 中移除
	redis.call('SREM', pending, taskId)
	redis.call('HDEL', started, taskId)
	
	return removed
`)

// AckTask removes a processed task from the processing queue, pending set, and started hash.
// 使用 task_id 匹配而非完整 JSON，避免序列化差异导致的匹配失败。
// 这允许该任务在下一个调度周期被重新推送。
func (c *Client) AckTask(ctx context.Context, task *pb.FetchRequest) error {
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

	// 使用 Lua 脚本原子执行
	_, err := ackTaskScript.Run(ctx, c.rdb,
		[]string{KeyTaskProcessingQueue, KeyTaskPendingSet, KeyTaskStartedHash},
		taskID,
	).Int()
	if err != nil {
		return fmt.Errorf("ack task script: %w", err)
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

// rescueScript 是用于原子性 rescue 任务的 Lua 脚本。
// 只有当 LREM 成功移除了任务时，才执行 LPUSH，防止多个 Janitor 重复添加。
// 同时清理 started hash 中的记录。
// KEYS[1] = processing queue, KEYS[2] = task queue, KEYS[3] = started hash
// ARGV[1] = task JSON, ARGV[2] = task_id
// 返回: 1 = 成功 rescue, 0 = 任务不存在
var rescueScript = redis.NewScript(`
	local removed = redis.call('LREM', KEYS[1], 1, ARGV[1])
	if removed > 0 then
		redis.call('LPUSH', KEYS[2], ARGV[1])
		redis.call('HDEL', KEYS[3], ARGV[2])
		return 1
	end
	return 0
`)

// RescueStuckTasks scans processing queue and requeues tasks that exceed timeout.
// 使用 KeyTaskStartedHash 中记录的开始时间来判断超时，而非任务的 CreatedAt。
// 使用 Lua 脚本确保原子性，防止多个 Janitor 同时处理同一任务导致重复入队。
func (c *Client) RescueStuckTasks(ctx context.Context, timeout time.Duration) (int, error) {
	if c == nil || c.rdb == nil {
		return 0, errors.New("redis client is not initialized")
	}

	// 获取所有任务的开始时间
	startedTimes, err := c.rdb.HGetAll(ctx, KeyTaskStartedHash).Result()
	if err != nil {
		return 0, fmt.Errorf("hgetall started: %w", err)
	}
	if len(startedTimes) == 0 {
		return 0, nil
	}

	// 获取 processing queue 中的任务
	tasksRaw, err := c.rdb.LRange(ctx, KeyTaskProcessingQueue, 0, -1).Result()
	if err != nil {
		return 0, fmt.Errorf("lrange processing: %w", err)
	}
	if len(tasksRaw) == 0 {
		// processing queue 为空，但 started hash 有记录，清理孤立记录
		for taskID := range startedTimes {
			c.rdb.HDel(ctx, KeyTaskStartedHash, taskID)
		}
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

		taskID := task.GetTaskId()
		if taskID == "" {
			continue
		}

		// 从 started hash 获取开始时间
		startedStr, ok := startedTimes[taskID]
		if !ok {
			// 没有记录开始时间，可能是老数据，使用 CreatedAt 作为后备
			if task.GetCreatedAt() == 0 {
				continue
			}
			if now-task.GetCreatedAt() <= threshold {
				continue
			}
		} else {
			// 使用开始时间判断
			var started int64
			if _, err := fmt.Sscanf(startedStr, "%d", &started); err != nil {
				continue
			}
			if now-started <= threshold {
				continue
			}
		}

		// 使用 Lua 脚本原子性地执行：只有 LREM 成功时才 LPUSH
		result, err := rescueScript.Run(ctx, c.rdb,
			[]string{KeyTaskProcessingQueue, KeyTaskQueue, KeyTaskStartedHash},
			raw, taskID,
		).Int()
		if err != nil {
			continue
		}
		if result == 1 {
			rescued++
		}
	}

	return rescued, nil
}

// DeduplicateQueue 清理任务队列中的重复任务，保留每个 task_id 的第一个条目。
// 返回移除的重复任务数量。
func (c *Client) DeduplicateQueue(ctx context.Context) (int, error) {
	if c == nil || c.rdb == nil {
		return 0, errors.New("redis client is not initialized")
	}

	// 获取队列中所有任务
	tasksRaw, err := c.rdb.LRange(ctx, KeyTaskQueue, 0, -1).Result()
	if err != nil {
		return 0, fmt.Errorf("lrange tasks: %w", err)
	}
	if len(tasksRaw) == 0 {
		return 0, nil
	}

	// 跟踪已见过的 task_id
	seen := make(map[string]bool)
	duplicates := []string{}

	for _, raw := range tasksRaw {
		var task pb.FetchRequest
		if err := protojson.Unmarshal([]byte(raw), &task); err != nil {
			continue
		}
		taskID := task.GetTaskId()
		if taskID == "" {
			continue
		}

		if seen[taskID] {
			// 已见过，标记为重复
			duplicates = append(duplicates, raw)
		} else {
			seen[taskID] = true
		}
	}

	// 移除所有重复项
	removed := 0
	for _, raw := range duplicates {
		count, err := c.rdb.LRem(ctx, KeyTaskQueue, 1, raw).Result()
		if err == nil && count > 0 {
			removed++
		}
	}

	return removed, nil
}

// RemoveFromPendingSet 从 pending set 中移除指定的 task_id。
// 用于删除任务时清理残留。
func (c *Client) RemoveFromPendingSet(ctx context.Context, taskID string) error {
	if c == nil || c.rdb == nil {
		return errors.New("redis client is not initialized")
	}
	if taskID == "" {
		return errors.New("task id is empty")
	}
	return c.rdb.SRem(ctx, KeyTaskPendingSet, taskID).Err()
}
