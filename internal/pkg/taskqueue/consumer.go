package taskqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"goodshunter/internal/pkg/metrics"

	"github.com/redis/go-redis/v9"
)

// Consumer 任务消费者，负责从队列中读取并处理任务。
//
// 由 Scheduler 服务使用，从 Redis Streams 中读取任务消息并执行。
type Consumer struct {
	queue            *TaskQueue
	logger           *slog.Logger
	groupName        string // 消费者组名称
	consumerID       string // 消费者唯一标识
	blockTime        time.Duration
	batchSize        int64
	pendingIdle      time.Duration
	pendingStart     string
	deadLetterStream string
	maxRetry         int
}

// FailureAction indicates how a failed message is handled.
type FailureAction string

const (
	FailureActionNone  FailureAction = "none"
	FailureActionRetry FailureAction = "retry"
	FailureActionDLQ   FailureAction = "dlq"
)

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

// WithPendingIdle 设置 Pending 消息的最小空闲时间。
func WithPendingIdle(d time.Duration) ConsumerOption {
	return func(c *Consumer) {
		c.pendingIdle = d
	}
}

// WithDeadLetterStream 设置死信 Stream 名称。
func WithDeadLetterStream(stream string) ConsumerOption {
	return func(c *Consumer) {
		c.deadLetterStream = stream
	}
}

// WithMaxRetry 设置最大重试次数。
func WithMaxRetry(maxRetry int) ConsumerOption {
	return func(c *Consumer) {
		c.maxRetry = maxRetry
	}
}

// NewConsumer 创建一个新的任务消费者。
//
// 会自动创建消费者组（如果不存在）。
//
// 参数:
//   - rdb: Redis 客户端
//   - logger: 日志记录器
//   - streamName: Stream 名称
//   - groupName: 消费者组名称
//   - consumerID: 消费者唯一标识（可选，默认为 "consumer-1"）
//   - opts: 可选配置
//
// 返回值:
//   - *Consumer: 消费者实例
//   - error: 创建失败时返回错误
func NewConsumer(rdb *redis.Client, logger *slog.Logger, streamName string, groupName string, consumerID string, opts ...ConsumerOption) (*Consumer, error) {
	if groupName == "" {
		return nil, fmt.Errorf("group name is required")
	}

	if consumerID == "" {
		consumerID = fmt.Sprintf("consumer-%d", time.Now().UnixNano())
	}

	if streamName == "" {
		streamName = "goodshunter:task:queue"
	}

	c := &Consumer{
		queue:            NewTaskQueue(rdb, logger, streamName),
		logger:           logger,
		groupName:        groupName,
		consumerID:       consumerID,
		blockTime:        1 * time.Second, // 默认阻塞1秒
		batchSize:        10,              // 默认每次读取10条
		pendingIdle:      1 * time.Minute,
		pendingStart:     "0-0",
		deadLetterStream: streamName + ":dlq",
		maxRetry:         3,
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
	pending, err := c.readPending(ctx)
	if err != nil {
		return nil, err
	}
	if len(pending) > 0 {
		return pending, nil
	}

	return c.readNew(ctx)
}

func (c *Consumer) readPending(ctx context.Context) ([]*MessageWithID, error) {
	messages, nextStart, err := c.queue.rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream:   c.queue.streamName,
		Group:    c.groupName,
		Consumer: c.consumerID,
		MinIdle:  c.pendingIdle,
		Start:    c.pendingStart,
		Count:    c.batchSize,
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("xautoclaim failed: %w", err)
	}
	if nextStart != "" {
		c.pendingStart = nextStart
	}

	if len(messages) > 0 {
		metrics.TaskAutoClaimTotal.Add(float64(len(messages)))
	}

	return c.parseMessages(ctx, messages)
}

func (c *Consumer) readNew(ctx context.Context) ([]*MessageWithID, error) {
	streams, err := c.queue.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    c.groupName,
		Consumer: c.consumerID,
		Streams:  []string{c.queue.streamName, ">"},
		Count:    c.batchSize,
		Block:    c.blockTime,
	}).Result()

	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("xreadgroup failed: %w", err)
	}

	if len(streams) == 0 {
		return nil, nil
	}

	var messages []redis.XMessage
	for _, stream := range streams {
		messages = append(messages, stream.Messages...)
	}

	return c.parseMessages(ctx, messages)
}

func (c *Consumer) parseMessages(ctx context.Context, messages []redis.XMessage) ([]*MessageWithID, error) {
	if len(messages) == 0 {
		return nil, nil
	}

	parsed := make([]*MessageWithID, 0, len(messages))
	for _, msg := range messages {
		data, ok := msg.Values["data"].(string)
		if !ok || data == "" {
			c.logger.Warn("invalid message format",
				slog.String("msg_id", msg.ID))
			c.handlePoisonMessage(ctx, msg.ID, fmt.Sprintf("%v", msg.Values["data"]), "invalid message format")
			continue
		}

		taskMsg, err := parseMessage(data)
		if err != nil {
			c.logger.Error("parse message failed",
				slog.String("msg_id", msg.ID),
				slog.String("error", err.Error()))
			c.handlePoisonMessage(ctx, msg.ID, data, err.Error())
			continue
		}

		parsed = append(parsed, &MessageWithID{
			ID:      msg.ID,
			Message: taskMsg,
		})
	}

	if len(parsed) > 0 {
		c.logger.Debug("messages read",
			slog.Int("count", len(parsed)))
	}

	return parsed, nil
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

// HandleFailure 根据重试次数进行重入队或放入死信队列。
func (c *Consumer) HandleFailure(ctx context.Context, msg *MessageWithID, cause error) (FailureAction, error) {
	if msg == nil || msg.Message == nil {
		return FailureActionNone, fmt.Errorf("message is nil")
	}

	retry := msg.Message.Retry + 1
	msg.Message.Retry = retry

	if retry > c.maxRetry {
		if err := c.publishDeadLetter(ctx, msg.ID, msg.Message, cause); err != nil {
			return FailureActionDLQ, err
		}
		return FailureActionDLQ, c.Ack(ctx, msg.ID)
	}

	if err := c.queue.Publish(ctx, msg.Message); err != nil {
		return FailureActionRetry, err
	}

	return FailureActionRetry, c.Ack(ctx, msg.ID)
}

func (c *Consumer) handlePoisonMessage(ctx context.Context, msgID string, payload string, reason string) {
	if err := c.publishDeadLetter(ctx, msgID, payload, errors.New(reason)); err != nil {
		c.logger.Error("publish dead letter failed", slog.String("msg_id", msgID), slog.String("error", err.Error()))
	}
	metrics.TaskDLQTotal.Inc()
	if err := c.Ack(ctx, msgID); err != nil {
		c.logger.Error("ack poison message failed", slog.String("msg_id", msgID), slog.String("error", err.Error()))
	}
}

func (c *Consumer) publishDeadLetter(ctx context.Context, msgID string, payload interface{}, cause error) error {
	raw := payload
	if msg, ok := payload.(*TaskMessage); ok {
		if data, err := json.Marshal(msg); err == nil {
			raw = string(data)
		}
	}

	return c.queue.publishRaw(ctx, c.deadLetterStream, map[string]interface{}{
		"original_id": msgID,
		"payload":     raw,
		"reason":      cause.Error(),
		"failed_at":   time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// Pending 获取待处理的消息数量。
func (c *Consumer) Pending(ctx context.Context) (int64, error) {
	info, err := c.queue.rdb.XPending(ctx, c.queue.streamName, c.groupName).Result()
	if err != nil {
		return 0, fmt.Errorf("xpending failed: %w", err)
	}
	return info.Count, nil
}
