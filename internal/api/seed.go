package api

import (
	"context"

	"fmt"
	"strconv"

	"goodshunter/internal/model"
	"goodshunter/proto/pb"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// SeedDemoData 初始化游客演示数据。
func (s *Server) SeedDemoData(ctx context.Context) error {
	const demoEmail = "demo@goodshunter.com"
	var user model.User
	err := s.db.WithContext(ctx).Where("email = ?", demoEmail).First(&user).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return err
	}
	if err == gorm.ErrRecordNotFound {
		hash, hashErr := bcrypt.GenerateFromPassword([]byte("guest-demo"), bcrypt.DefaultCost)
		if hashErr != nil {
			return hashErr
		}
		user = model.User{
			Email:      demoEmail,
			Password:   string(hash),
			Role:       "guest",
			IsVerified: true,
		}
		if err := s.db.WithContext(ctx).Create(&user).Error; err != nil {
			return err
		}
	} else {
		updates := map[string]interface{}{
			"role":        "guest",
			"is_verified": true,
		}
		if err := s.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", user.ID).Updates(updates).Error; err != nil {
			return err
		}
	}

	// 自动迁移：将旧的演示任务重命名为新的，并重置状态
	s.db.WithContext(ctx).Model(&model.Task{}).
		Where("user_id = ? AND keyword = ?", user.ID, "HimeHina GiGO").
		Updates(map[string]interface{}{
			"keyword":     "進撃の巨人",
			"baseline_at": nil,
			"last_run_at": nil,
		})

	var task model.Task
	taskErr := s.db.WithContext(ctx).
		Where("user_id = ? AND keyword = ?", user.ID, "進撃の巨人").
		First(&task).Error
	if taskErr != nil && taskErr != gorm.ErrRecordNotFound {
		return taskErr
	}
	if taskErr == gorm.ErrRecordNotFound {
		task = model.Task{
			UserID:        user.ID,
			Keyword:       "進撃の巨人",
			MinPrice:      0,
			MaxPrice:      0,
			Platform:      int(pb.Platform_PLATFORM_MERCARI),
			SortBy:        int(pb.SortBy_SORT_BY_CREATED_TIME),
			Status:        "running",
			NotifyEnabled: false,
		}
		if err := s.db.WithContext(ctx).Create(&task).Error; err != nil {
			return err
		}
	} else {
		// 确保已有任务的配置也是最新的
		updates := map[string]interface{}{
			"min_price":      0,
			"max_price":      0,
			"notify_enabled": false,
		}
		// 如果关键词已经是“進撃の巨人”，我们也应该检查是否需要重置 Baseline
		// 但为了保险起见，如果是刚从“HimeHina”迁移过来的，上面的 Update 已经置空了
		// 这里主要处理“已经是進撃の巨人但价格配置不对”的情况
		// 如果想强制重置访客任务状态（例如手动触发修复），可以在这里也置空 baseline
		// 但正常情况下，只要关键词没变，我们不希望每次重启都重置 Baseline（否则已有的 New 会消失或重置逻辑）
		// 不过既然用户反馈“全是 New”，说明可能 Baseline 还没对上。
		// 最安全的方式是：如果是演示任务，且我们想强制它是 Clean Slate，就 Reset。
		// 对于演示模式，每次重启服务都重置成 Initial State 也许是合理的？
		// 为了解决用户当前的问题，我们强制重置一次 Baseline。
		updates["baseline_at"] = nil
		updates["last_run_at"] = nil

		if err := s.db.WithContext(ctx).Model(&model.Task{}).Where("id = ?", task.ID).Updates(updates).Error; err != nil {
			return err
		}
	}

	// 强制清理 Redis 缓存，防止显示旧关键词的商品
	taskIDStr := strconv.FormatUint(uint64(task.ID), 10)
	keys := []string{
		"guest:task:" + taskIDStr + ":items",
		"dedup:" + taskIDStr,
	}
	if err := s.rdb.Del(ctx, keys...).Err(); err != nil {
		fmt.Printf("failed to clear redis cache for guest task: %v\n", err)
	}

	return nil
}
