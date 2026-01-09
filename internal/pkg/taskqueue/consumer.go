package taskqueue

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// Consumer 任务消费者，负责从队列中读取并处理任务。
//
// 由 Scheduler 服务使用，从 Redis Streams 中读取任务消息并执行。
type Consumer struct {
	queue      *TaskQueue
	logger     *slog.Logger
	groupName  string // 消费者组名称
	consumerID string // 消费者唯一标识
	blockTime  time.Duration
	batchSize  int64
}

// GroupName 返回消费者组名称。
func (c *Consumer) GroupName() string {
	return c.groupName
}

// ConsumerOption 消费者配置选项。
type ConsumerOption func(*Consumer)

// WithBlockTime 设置阻塞等待时间。
func WithBlockTime(d time.Duration) ConsumerOption {
	return func(c *Consumer) {
		c.blockTime = d
	}
}

// WithBatchSize 设置每次读取的消息数量。
func WithBatchSize(size int64) ConsumerOption {
	return func(c *Consumer) {
		c.batchSize = size
	}
}

// NewConsumer 创建一个新的任务消费者。
//
// 会自动创建消费者组（如果不存在）。
//
// 参数:
//   - rdb: Redis 客户端
//   - logger: 日志记录器
//   - groupName: 消费者组名称
//   - consumerID: 消费者唯一标识（可选，默认为 "consumer-1"）
//   - opts: 可选配置
//
// 返回值:
//   - *Consumer: 消费者实例
//   - error: 创建失败时返回错误
func NewConsumer(rdb *redis.Client, logger *slog.Logger, groupName string, consumerID string, opts ...ConsumerOption) (*Consumer, error) {
	if groupName == "" {
		return nil, fmt.Errorf("group name is required")
	}

	if consumerID == "" {
		consumerID = fmt.Sprintf("consumer-%d", time.Now().UnixNano())
	}

	c := &Consumer{
		queue:      NewTaskQueue(rdb, logger, "goodshunter:task:queue"),
		logger:     logger,
		groupName:  groupName,
		consumerID: consumerID,
		blockTime:  1 * time.Second, // 默认阻塞1秒
		batchSize:  10,              // 默认每次读取10条
	}

	// 应用配置选项
	for _, opt := range opts {
		opt(c)
	}

	// 创建消费者组
	if err := c.queue.CreateConsumerGroup(context.Background(), groupName); err != nil {
		return nil, err
	}

	c.logger.Info("consumer created",
		slog.String("group", groupName),
		slog.String("consumer_id", consumerID))

	return c, nil
}

// MessageWithID 包含消息 ID 的任务消息。
type MessageWithID struct {
	ID      string       // Redis Stream 消息 ID
	Message *TaskMessage // 任务消息内容
}

// Read 从队列中读取任务消息。
//
// 使用 XREADGROUP 命令从消费者组中读取消息。
//
// 参数:
//   - ctx: 上下文
//
// 返回值:
//   - []*MessageWithID: 读取到的消息列表
//   - error: 读取失败时返回错误
func (c *Consumer) Read(ctx context.Context) ([]*MessageWithID, error) {
	// 使用 XREADGROUP 读取消息
	// ">" 表示读取未被消费的新消息
	streams, err := c.queue.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    c.groupName,
		Consumer: c.consumerID,
		Streams:  []string{c.queue.streamName, ">"},
		Count:    c.batchSize,
		Block:    c.blockTime,
	}).Result()

	if err != nil {
		if err == redis.Nil {
			// 没有新消息，返回空列表
			return nil, nil
		}
		return nil, fmt.Errorf("xreadgroup failed: %w", err)
	}

	if len(streams) == 0 {
		return nil, nil
	}

	var messages []*MessageWithID
	for _, stream := range streams {
		for _, msg := range stream.Messages {
			// 解析消息数据
			data, ok := msg.Values["data"].(string)
			if !ok {
				c.logger.Warn("invalid message format",
					slog.String("msg_id", msg.ID))
				continue
			}

			taskMsg, err := parseMessage(data)
			if err != nil {
				c.logger.Error("parse message failed",
					slog.String("msg_id", msg.ID),
					slog.String("error", err.Error()))
				continue
			}

			messages = append(messages, &MessageWithID{
				ID:      msg.ID,
				Message: taskMsg,
			})
		}
	}

	if len(messages) > 0 {
		c.logger.Debug("messages read",
			slog.Int("count", len(messages)))
	}

	return messages, nil
}

// Ack 确认消息已处理。
//
// 使用 XACK 命令告知 Redis 该消息已成功处理。
//
// 参数:
//   - ctx: 上下文
//   - msgID: 消息 ID
//
// 返回值:
//   - error: 确认失败时返回错误
func (c *Consumer) Ack(ctx context.Context, msgID string) error {
	acked, err := c.queue.rdb.XAck(ctx, c.queue.streamName, c.groupName, msgID).Result()
	if err != nil {
		return fmt.Errorf("xack failed: %w", err)
	}

	if acked == 0 {
		c.logger.Warn("message not acked (may already be acked)",
			slog.String("msg_id", msgID))
	}

	return nil
}

// AckBatch 批量确认多条消息。
func (c *Consumer) AckBatch(ctx context.Context, msgIDs []string) error {
	if len(msgIDs) == 0 {
		return nil
	}

	acked, err := c.queue.rdb.XAck(ctx, c.queue.streamName, c.groupName, msgIDs...).Result()
	if err != nil {
		return fmt.Errorf("xack batch failed: %w", err)
	}

	c.logger.Debug("messages acked",
		slog.Int("count", int(acked)),
		slog.Int("requested", len(msgIDs)))

	return nil
}

// Pending 获取待处理的消息数量。
func (c *Consumer) Pending(ctx context.Context) (int64, error) {
	info, err := c.queue.rdb.XPending(ctx, c.queue.streamName, c.groupName).Result()
	if err != nil {
		return 0, fmt.Errorf("xpending failed: %w", err)
	}
	return info.Count, nil
}
