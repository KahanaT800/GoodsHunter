package notify

import (
	"context"

	"goodshunter/internal/model"
)

// Notifier 定义通知接口。
type Notifier interface {
	// Send 发送通知。
	//
	// 参数:
	//   ctx: 上下文
	//   item: 商品信息
	//   reason: 通知原因 (如 "Found New Item", "Price Drop Detected")
	//   keyword: 任务关键词
	//   oldPrice: 降价前价格（非降价时传 0）
	//   toEmail: 接收邮箱
	Send(ctx context.Context, item *model.Item, reason string, keyword string, oldPrice int32, toEmail string) error
}
