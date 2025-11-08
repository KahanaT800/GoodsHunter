package taskqueue

import "time"

// TaskMessage 表示任务队列中的消息结构。
//
// 用于在 Redis Streams 中传递任务调度信息。
type TaskMessage struct {
	TaskID    uint      `json:"task_id"`   // 任务 ID
	Action    string    `json:"action"`    // 操作类型: "execute" (执行任务), "stop" (停止任务)
	Timestamp time.Time `json:"timestamp"` // 消息创建时间
	Retry     int       `json:"retry"`     // 重试次数
	Source    string    `json:"source"`    // 消息来源: "user_create" (用户创建), "periodic" (周期调度)
}

// NewExecuteMessage 创建一个执行任务的消息。
func NewExecuteMessage(taskID uint, source string) *TaskMessage {
	return &TaskMessage{
		TaskID:    taskID,
		Action:    "execute",
		Timestamp: time.Now(),
		Retry:     0,
		Source:    source,
	}
}

// NewStopMessage 创建一个停止任务的消息。
func NewStopMessage(taskID uint) *TaskMessage {
	return &TaskMessage{
		TaskID:    taskID,
		Action:    "stop",
		Timestamp: time.Now(),
		Retry:     0,
		Source:    "system",
	}
}
