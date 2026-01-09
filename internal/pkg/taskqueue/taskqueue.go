package taskqueue

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"
)

// TaskQueue 封装 Redis Streams 的任务队列操作。
//
// 提供任务的发布、消费、确认等核心功能。
type TaskQueue struct {
	rdb        *redis.Client
	logger     *slog.Logger
	streamName string // Stream 名称，如 "goodshunter:task:queue"
}

// NewTaskQueue 创建一个新的任务队列实例。
//
// 参数:
//   - rdb: Redis 客户端
//   - logger: 日志记录器
//   - streamName: Stream 名称
//
// 返回值:
//   - *TaskQueue: 任务队列实例
func NewTaskQueue(rdb *redis.Client, logger *slog.Logger, streamName string) *TaskQueue {
	if streamName == "" {
		streamName = "goodshunter:task:queue"
	}
	return &TaskQueue{
		rdb:        rdb,
		logger:     logger,
		streamName: streamName,
	}
}

// Publish 发布一条任务消息到队列。
//
// 使用 XADD 命令将消息追加到 Stream。
//
// 参数:
//   - ctx: 上下文
//   - msg: 任务消息
//
// 返回值:
//   - error: 发布失败时返回错误
func (q *TaskQueue) Publish(ctx context.Context, msg *TaskMessage) error {
	if msg == nil {
		return fmt.Errorf("message is nil")
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	return q.publishRaw(ctx, q.streamName, map[string]interface{}{
		"data": string(data),
	})
}

func (q *TaskQueue) publishRaw(ctx context.Context, stream string, values map[string]interface{}) error {
	args := &redis.XAddArgs{
		Stream: stream,
		MaxLen: 100000,
		Approx: false,
		Values: values,
	}

	msgID, err := q.rdb.XAdd(ctx, args).Result()
	if err != nil {
		return fmt.Errorf("xadd failed: %w", err)
	}

	q.logger.Debug("task message published",
		slog.String("stream", stream),
		slog.String("msg_id", msgID),
		slog.Any("fields", values))

	return nil
}

// CreateConsumerGroup 创建消费者组。
//
// 使用 XGROUP CREATE 命令创建消费者组，如果已存在则忽略。
//
// 参数:
//   - ctx: 上下文
//   - groupName: 消费者组名称
//
// 返回值:
//   - error: 创建失败时返回错误
func (q *TaskQueue) CreateConsumerGroup(ctx context.Context, groupName string) error {
	// 尝试创建消费者组，从 Stream 起始位置开始消费
	err := q.rdb.XGroupCreateMkStream(ctx, q.streamName, groupName, "0").Err()
	if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		return fmt.Errorf("create consumer group: %w", err)
	}

	q.logger.Info("consumer group ready",
		slog.String("stream", q.streamName),
		slog.String("group", groupName))

	return nil
}

// StreamInfo 获取 Stream 的基本信息。
//
// 返回值:
//   - length: Stream 中消息数量
//   - error: 查询失败时返回错误
func (q *TaskQueue) StreamInfo(ctx context.Context) (int64, error) {
	length, err := q.rdb.XLen(ctx, q.streamName).Result()
	if err != nil {
		return 0, fmt.Errorf("xlen failed: %w", err)
	}
	return length, nil
}

// parseMessage 解析 Redis Stream 消息。
func parseMessage(data string) (*TaskMessage, error) {
	var msg TaskMessage
	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		return nil, fmt.Errorf("unmarshal message: %w", err)
	}
	return &msg, nil
}
