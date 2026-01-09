package taskqueue

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"
)

// Producer 任务生产者，负责发布任务到队列。
//
// 由 API 服务使用，将用户创建的任务或系统触发的任务发布到 Redis Streams。
type Producer struct {
	queue  *TaskQueue
	logger *slog.Logger
}

// NewProducer 创建一个新的任务生产者。
//
// 参数:
//   - rdb: Redis 客户端
//   - logger: 日志记录器
//   - streamName: Stream 名称（可选，默认为 "goodshunter:task:queue"）
//
// 返回值:
//   - *Producer: 生产者实例
func NewProducer(rdb *redis.Client, logger *slog.Logger, streamName ...string) *Producer {
	stream := "goodshunter:task:queue"
	if len(streamName) > 0 && streamName[0] != "" {
		stream = streamName[0]
	}

	return &Producer{
		queue:  NewTaskQueue(rdb, logger, stream),
		logger: logger,
	}
}

// SubmitTask 提交一个任务到队列等待执行。
//
// 用于用户创建任务或周期调度时调用。
//
// 参数:
//   - ctx: 上下文
//   - taskID: 任务 ID
//   - source: 任务来源（"user_create" 或 "periodic"）
//
// 返回值:
//   - error: 提交失败时返回错误
func (p *Producer) SubmitTask(ctx context.Context, taskID uint, source string) error {
	if taskID == 0 {
		return fmt.Errorf("invalid task id: %d", taskID)
	}

	if source == "" {
		source = "unknown"
	}

	msg := NewExecuteMessage(taskID, source)
	if err := p.queue.Publish(ctx, msg); err != nil {
		p.logger.Error("submit task failed",
			slog.Uint64("task_id", uint64(taskID)),
			slog.String("source", source),
			slog.String("error", err.Error()))
		return err
	}

	p.logger.Info("task submitted",
		slog.Uint64("task_id", uint64(taskID)),
		slog.String("source", source))

	return nil
}

// StopTask 发送停止任务的消息。
//
// 用于用户手动停止任务或系统自动停止时调用。
//
// 参数:
//   - ctx: 上下文
//   - taskID: 任务 ID
//
// 返回值:
//   - error: 发送失败时返回错误
func (p *Producer) StopTask(ctx context.Context, taskID uint) error {
	if taskID == 0 {
		return fmt.Errorf("invalid task id: %d", taskID)
	}

	msg := NewStopMessage(taskID)
	if err := p.queue.Publish(ctx, msg); err != nil {
		p.logger.Error("stop task failed",
			slog.Uint64("task_id", uint64(taskID)),
			slog.String("error", err.Error()))
		return err
	}

	p.logger.Info("stop task message sent",
		slog.Uint64("task_id", uint64(taskID)))

	return nil
}

// QueueLength 获取当前队列长度。
func (p *Producer) QueueLength(ctx context.Context) (int64, error) {
	return p.queue.StreamInfo(ctx)
}
