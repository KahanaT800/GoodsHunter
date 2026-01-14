package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"goodshunter/internal/model"
	"goodshunter/internal/pkg/metrics"
	"goodshunter/internal/pkg/notify"
	"goodshunter/internal/pkg/queue"
	"goodshunter/internal/pkg/redisqueue"
	"goodshunter/proto/pb"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Scheduler 负责管理所有任务的独立调度器。
//
// 每个任务都有自己的 goroutine 和 ticker，创建任务时立即启动，
// 停止或删除任务时自动停止对应的调度循环。
type Scheduler struct {
	db              *gorm.DB
	rdb             *redis.Client
	logger          *slog.Logger
	interval        time.Duration
	newItemDuration time.Duration
	maxItemsPerTask int
	queue           *queue.Queue
	notifier        notify.Notifier
	redisQueue      *redisqueue.Client
	batchSize       int // 轮询批量入队大小
	janitorInterval time.Duration
	janitorTimeout  time.Duration
}

// NewScheduler 创建一个新的调度器实例。
//
// 参数:
//
//	db: MySQL 数据库连接
//	rdb: Redis 客户端
//	logger: 日志记录器
//	redisQueue: Redis List 队列客户端
//	interval: 调度循环的时间间隔
//	newItemDuration: 新商品热度持续时间
//	maxItems: 每个任务保留的最大商品数
//	workers: Worker Pool 大小（并发任务数，0 表示使用默认值 50）
//	capacity: 队列容量（0 表示使用默认值 1000）
//
// 返回值:
//
//	*Scheduler: 调度器实例
func NewScheduler(db *gorm.DB, rdb *redis.Client, logger *slog.Logger, redisQueue *redisqueue.Client, notifier notify.Notifier, interval time.Duration, newItemDuration time.Duration, maxItems int, workers int, capacity int, batchSize int, janitorInterval time.Duration, janitorTimeout time.Duration) *Scheduler {
	// 使用默认值（如果参数为 0）
	if workers <= 0 {
		workers = 50 // 默认 50 个并发 worker
	}
	if maxItems <= 0 {
		maxItems = 300
	}
	if capacity <= 0 {
		capacity = 1000 // 默认队列容量 1000
	}

	q := queue.NewQueue(logger, workers, capacity)

	// 设置错误处理器：记录任务执行失败
	q.SetErrorHandler(func(err error, job queue.Job) {
		logger.Error("crawler task execution failed",
			slog.String("error", err.Error()))
	})

	if batchSize <= 0 {
		batchSize = 100
	}
	if janitorInterval <= 0 {
		janitorInterval = 10 * time.Minute
	}
	if janitorTimeout <= 0 {
		janitorTimeout = 30 * time.Minute
	}

	return &Scheduler{
		db:              db,
		rdb:             rdb,
		logger:          logger,
		interval:        interval,
		newItemDuration: newItemDuration,
		maxItemsPerTask: maxItems,
		queue:           q,
		notifier:        notifier,
		redisQueue:      redisQueue,
		batchSize:       batchSize,
		janitorInterval: janitorInterval,
		janitorTimeout:  janitorTimeout,
	}
}

// Run 启动调度器，加载所有 running 状态的任务。
//
// 这个方法会在服务启动时被调用，扫描数据库中所有 status='running' 的任务，
// 为每个任务启动独立的调度循环。
//
// 参数:
//
//	ctx: 用于控制停止的上下文
func (s *Scheduler) Run(ctx context.Context) {
	s.DispatchTasks(ctx)
}

// DispatchTasks 轮询数据库并将任务派发到 Redis List 队列。
func (s *Scheduler) DispatchTasks(ctx context.Context) {
	if s.redisQueue == nil {
		s.logger.Error("redis queue client is not initialized")
		return
	}
	s.logger.Info("scheduler dispatcher started",
		slog.String("mode", "redis_list"),
		slog.String("interval", s.interval.String()),
		slog.Int("workers", s.queue.Cap()/20),
		slog.Int("queue_capacity", s.queue.Cap()))

	// 启动 Worker Pool
	s.queue.Start(ctx)

	// 首次立即调度一次
	s.enqueueRunningTasks(ctx)

	// 定时调度循环
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// 定期打印队列统计（每分钟）
	statsTicker := time.NewTicker(1 * time.Minute)
	defer statsTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("scheduler stopping")
			// 优雅关闭：等待所有任务完成，最多等待 30 秒
			if err := s.queue.ShutdownWithTimeout(30 * time.Second); err != nil {
				s.logger.Error("queue shutdown timeout", slog.String("error", err.Error()))
			}
			s.logger.Info("scheduler stopped")
			return

		case <-ticker.C:
			s.enqueueRunningTasks(ctx)

		case <-statsTicker.C:
			// 打印队列统计信息
			s.printQueueStats()
		}
	}
}

// internal/api/scheduler/scheduler.go

// StartResultListener 监听 Redis 结果队列并处理抓取结果。
func (s *Scheduler) StartResultListener(ctx context.Context) error {
	if s.redisQueue == nil {
		return errors.New("redis queue client is not initialized")
	}

	s.logger.Info("result listener started")
	go s.monitorQueueDepth(ctx)

	// 限制 API 端并发处理结果的数量（例如 10 并发）
	sem := make(chan struct{}, 10)

	for {
		// 1. 申请令牌
		select {
		case sem <- struct{}{}:
			// 获得令牌，继续
		case <-ctx.Done():
			return nil // 正常退出
		}

		// 2. 拉取结果
		res, err := s.redisQueue.PopResult(ctx, 2*time.Second)
		if err != nil {
			// 归还令牌
			<-sem
			if errors.Is(err, redisqueue.ErrNoResult) {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			// 优雅关闭：返回 nil 而不是 err，防止 main 函数打印 "error: context canceled"
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			s.logger.Error("pop redis result failed", slog.String("error", err.Error()))
			time.Sleep(200 * time.Millisecond)
			continue
		}

		if res == nil {
			<-sem // 防御性编程：空结果也归还
			continue
		}

		// 3. 并发处理入库
		go func(resp *pb.FetchResponse) {
			// 任务结束时归还令牌
			defer func() { <-sem }()

			defer func() {
				if r := recover(); r != nil {
					// 如果代码运行到这里，说明发生了严重的 Panic！
					// 1. 我们捕获它，不让它向上冒泡炸掉整个进程。
					// 2. 我们记录下是谁（task_id）导致了崩溃，以及崩溃原因（r）。
					s.logger.Error("PANIC in result handler",
						slog.Any("panic", r), // 打印崩溃原因（如 "runtime error: ...")
						slog.String("task_id", resp.GetTaskId()))

					// 3. 函数在这里优雅结束，Goroutine 退出，但 API 进程还在！
				}
			}()

			// 使用 Background context 防止入库操作被外层 cancel 中断（导致半个事务）
			if err := s.handleResult(context.Background(), resp); err != nil {
				s.logger.Error("result processing failed", slog.String("error", err.Error()))
			} else {
				s.logger.Info("result processed",
					slog.String("task_id", resp.GetTaskId()),
					slog.Int("items", len(resp.GetItems())))
			}
		}(res)
	}
}

// StartJanitor runs a periodic rescue loop for stuck tasks.
func (s *Scheduler) StartJanitor(ctx context.Context) {
	ticker := time.NewTicker(s.janitorInterval)
	s.logger.Info("janitor started")

	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runRescue(ctx)
			}
		}
	}()
}

func (s *Scheduler) runRescue(ctx context.Context) {
	if s.redisQueue == nil {
		return
	}
	timeout := s.janitorTimeout
	rescueCtx, cancel := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel()

	count, err := s.redisQueue.RescueStuckTasks(rescueCtx, timeout)
	if err != nil {
		s.logger.Error("janitor failed to rescue tasks", slog.String("error", err.Error()))
		return
	}
	if count > 0 {
		s.logger.Info("janitor rescued stuck tasks", slog.Int("count", count))
	}

	// 额外保险：如果历史上仍有卡在 queued 状态过久的任务，将其恢复为 running，
	// 让调度器可以重新调度。这主要用于兼容老数据或未来潜在的状态异常。
	if err := s.db.WithContext(rescueCtx).
		Model(&model.Task{}).
		Where("status = ? AND updated_at < ?", "queued", time.Now().Add(-timeout)).
		Update("status", "running").Error; err != nil {
		s.logger.Warn("janitor failed to reset queued tasks",
			slog.String("error", err.Error()))
	}
}

func (s *Scheduler) monitorQueueDepth(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tasks, results, err := s.redisQueue.QueueDepth(ctx)
			if err != nil {
				s.logger.Warn("queue depth probe failed", slog.String("error", err.Error()))
				continue
			}
			metrics.CrawlerQueueDepth.WithLabelValues("tasks").Set(float64(tasks))
			metrics.CrawlerQueueDepth.WithLabelValues("results").Set(float64(results))
		}
	}
}

func (s *Scheduler) handleResult(ctx context.Context, resp *pb.FetchResponse) error {
	taskID, err := parseTaskID(resp.GetTaskId())
	if err != nil {
		s.logger.Warn("invalid task id in result", slog.String("task_id", resp.GetTaskId()), slog.String("error", err.Error()))
		return err
	}

	var task model.Task
	if err := s.db.WithContext(ctx).Where("id = ?", taskID).First(&task).Error; err != nil {
		s.logger.Warn("load task failed", slog.String("task_id", intToString(taskID)), slog.String("error", err.Error()))
		return err
	}

	status, message := classifyResult(resp)
	s.setTaskCrawlStatus(ctx, task.ID, status, message)

	firstRun := task.BaselineAt == nil || task.BaselineAt.IsZero()
	notifyAllowed := !firstRun && task.NotifyEnabled
	userEmail := ""
	userRole := ""
	{
		var user model.User
		if err := s.db.Select("email", "role").Where("id = ?", task.UserID).First(&user).Error; err != nil {
			s.logger.Warn("load user info failed", slog.String("task_id", intToString(task.ID)), slog.String("error", err.Error()))
		} else {
			userEmail = user.Email
			userRole = strings.ToLower(strings.TrimSpace(user.Role))
		}
	}

	if userRole == "guest" {
		key := fmt.Sprintf("guest:active:%d", task.UserID)
		if _, err := s.rdb.Get(ctx, key).Result(); err == redis.Nil {
			if err := s.db.Model(&model.Task{}).Where("id = ?", task.ID).Update("status", "stopped").Error; err != nil {
				s.logger.Warn("stop guest task failed", slog.String("task_id", intToString(task.ID)), slog.String("error", err.Error()))
			} else {
				s.logger.Info("guest task stopped due to inactivity", slog.String("task_id", intToString(task.ID)))
			}
			return nil
		}
	}

	// 统一走 DB 持久化逻辑，不再区分 Guest
	if status != "failed" {
		s.processItems(&task, resp.Items, notifyAllowed, userEmail)
	}

	now := time.Now()
	updates := map[string]interface{}{
		"last_run_at": now,
	}
	if firstRun && status != "failed" {
		updates["baseline_at"] = now
		s.logger.Info("baseline_at set for task",
			slog.Uint64("task_id", uint64(task.ID)),
			slog.String("status", status),
			slog.Int("items_count", len(resp.Items)),
		)
	}
	if err := s.db.Model(&model.Task{}).Where("id = ?", task.ID).Updates(updates).Error; err != nil {
		s.logger.Warn("update run timestamps failed", slog.String("task_id", intToString(task.ID)), slog.String("error", err.Error()))
	}

	finalStatus := "running"
	if task.Type == model.TaskTypeOneShot {
		finalStatus = "completed"
	}
	if err := s.db.WithContext(ctx).Model(&model.Task{}).Where("id = ?", task.ID).Update("status", finalStatus).Error; err != nil {
		s.logger.Warn("update task status failed", slog.String("task_id", intToString(task.ID)), slog.String("error", err.Error()))
	}
	return nil
}

func classifyResult(resp *pb.FetchResponse) (string, string) {
	if resp == nil {
		return "failed", "empty response"
	}
	if resp.GetErrorMessage() != "" && resp.GetErrorMessage() != "no_items" {
		return "failed", resp.GetErrorMessage()
	}
	if resp.GetErrorMessage() == "no_items" || len(resp.GetItems()) == 0 {
		return "no_items", "no items found"
	}
	return "success", ""
}

// StartTask 将任务放入队列，等待 worker 执行。
func (s *Scheduler) StartTask(parentCtx context.Context, task *model.Task) {
	if task == nil || task.Status != "running" {
		return
	}
	s.enqueueTaskID(parentCtx, task.ID)
}

// StopTask 仅记录日志，实际停止由任务状态控制。
func (s *Scheduler) StopTask(taskID uint) {
	s.logger.Info("task scheduler stopped", slog.Uint64("task_id", uint64(taskID)))
}

// enqueueRunningTasks 查询并入队所有 running 任务。
func (s *Scheduler) enqueueRunningTasks(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	batchSize := s.batchSize
	if batchSize <= 0 {
		batchSize = 100
	}
	if capLimit := s.queue.Cap() / 5; capLimit > 0 && batchSize > capLimit {
		batchSize = capLimit
	}

	var lastID uint
	for {
		if ctx.Err() != nil {
			return
		}

		var taskIDs []uint
		if err := s.db.WithContext(ctx).
			Model(&model.Task{}).
			Select("id").
			Where("status = ? AND id > ?", "running", lastID).
			Order("id ASC").
			Limit(batchSize).
			Pluck("id", &taskIDs).Error; err != nil {
			s.logger.Error("failed to load running tasks", slog.String("error", err.Error()))
			return
		}

		if len(taskIDs) == 0 {
			return
		}

		for _, id := range taskIDs {
			if ctx.Err() != nil {
				return
			}
			s.enqueueTaskID(ctx, id)
			lastID = id
		}
	}
}

func (s *Scheduler) enqueueTaskID(ctx context.Context, taskID uint) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.queue.EnqueueBlocking(ctx, func(ctx context.Context) error {
		return s.handleTaskByID(ctx, taskID)
	}); err != nil {
		s.logger.Warn("enqueue task blocked or canceled",
			slog.Uint64("task_id", uint64(taskID)),
			slog.String("error", err.Error()),
			slog.Int("queue_len", s.queue.Len()),
			slog.Int("queue_cap", s.queue.Cap()))
	}
}

func (s *Scheduler) handleTaskByID(parentCtx context.Context, taskID uint) error {
	var task model.Task
	if err := s.db.Where("id = ? AND status = ?", taskID, "running").First(&task).Error; err != nil {
		return nil
	}
	return s.handleTask(parentCtx, &task)
}

// handleTask 将任务派发到 Redis List 队列。
func (s *Scheduler) handleTask(parentCtx context.Context, task *model.Task) error {
	if task == nil {
		return nil
	}
	if s.redisQueue == nil {
		return errors.New("redis queue client is not initialized")
	}

	req := &pb.FetchRequest{
		TaskId:     intToString(task.ID),
		Platform:   pb.Platform(task.Platform),
		Keyword:    task.Keyword,
		MinPrice:   task.MinPrice,
		MaxPrice:   task.MaxPrice,
		OnlyOnSale: true,
		Sort:       pb.SortBy(task.SortBy),
		Order:      pb.SortOrder(task.SortOrder),
		CreatedAt:  time.Now().Unix(),
	}

	if err := s.redisQueue.PushTask(parentCtx, req); err != nil {
		s.logger.Error("push task failed",
			slog.String("task_id", req.TaskId),
			slog.String("error", err.Error()))
		return err
	}

	s.logger.Info("task pushed to redis queue",
		slog.String("task_id", req.TaskId),
		slog.String("keyword", task.Keyword),
		slog.Int("platform", task.Platform))
	return nil
}

func (s *Scheduler) setTaskCrawlStatus(ctx context.Context, taskID uint, status, message string) {
	if status == "" {
		return
	}
	key := "task:crawl_status:" + intToString(taskID)
	msgKey := "task:crawl_message:" + intToString(taskID)
	ttl := 24 * time.Hour
	if err := s.rdb.Set(ctx, key, status, ttl).Err(); err != nil {
		s.logger.Warn("set crawl status failed", slog.String("task_id", intToString(taskID)), slog.String("error", err.Error()))
	}
	if message != "" {
		if err := s.rdb.Set(ctx, msgKey, message, ttl).Err(); err != nil {
			s.logger.Warn("set crawl message failed", slog.String("task_id", intToString(taskID)), slog.String("error", err.Error()))
		}
	} else {
		if err := s.rdb.Del(ctx, msgKey).Err(); err != nil && err != redis.Nil {
			s.logger.Warn("delete crawl message failed", slog.String("task_id", intToString(taskID)), slog.String("error", err.Error()))
		}
	}
}

// processItems 处理抓取到的商品列表（核心去重与优化逻辑）。
//
// 使用 Redis Hash 存储商品价格: Key: dedup:{task_id}, Field: source_id, Value: price
//
// 策略调整：
// 为了解决由抓取深度扩展（如从 15->50）导致的“旧商品被误判为新品”问题。
// 我们维护一个 lastKnownTime。
// - 如果遇到已知的旧商品：更新 lastKnownTime 为该旧商品的 CreatedAt（首次发现时间）。
// - 如果遇到未知的新商品：
//   - 如果之前尚未遇到过旧商品（列表头部），则判定为“真正的新品”，使用当前 batchTime。
//   - 如果之前已经遇到过旧商品（列表尾部），则判定为“因抓取更深而发现的历史商品”，使用 lastKnownTime。
//     这确保它们在时间轴排序中位于旧商品之后（Rank 更大）。
func (s *Scheduler) processItems(task *model.Task, items []*pb.Item, notify bool, userEmail string) {
	ctx := context.Background()
	dedupKey := "dedup:" + intToString(task.ID)

	batchTime := time.Now()
	var lastKnownTime time.Time // 用于锚定历史商品的时间戳

	for idx, it := range items {
		if strings.TrimSpace(it.SourceId) == "" {
			s.logger.Warn("skip item with empty source_id", slog.String("task_id", intToString(task.ID)), slog.String("title", it.Title))
			continue
		}

		oldPriceStr, err := s.rdb.HGet(ctx, dedupKey, it.SourceId).Result()
		if err != nil && err != redis.Nil {
			s.logger.Error("redis hget failed", slog.String("task_id", intToString(task.ID)), slog.String("source_id", it.SourceId), slog.String("error", err.Error()))
			continue
		}

		isNew := err == redis.Nil
		newPrice := it.Price

		// 确定用于关联的时间戳
		linkTime := batchTime
		if !isNew {
			// 如果是旧商品，查询其原始的关联时间
			var ti model.TaskItem
			// 先获取 ItemID
			var item model.Item
			if err := s.db.Select("id").Where("source_id = ?", it.SourceId).First(&item).Error; err == nil {
				if err := s.db.Where("task_id = ? AND item_id = ?", task.ID, item.ID).First(&ti).Error; err == nil {
					lastKnownTime = ti.CreatedAt
				}
			}
		} else {
			// 如果是新商品，且我们已有历史锚点，则复用历史锚点
			if !lastKnownTime.IsZero() {
				linkTime = lastKnownTime
			}
		}

		// 1. 处理新商品
		if isNew {
			itemID, err := s.upsertItem(ctx, it)
			if err != nil {
				s.logger.Error("insert new item failed", slog.String("task_id", intToString(task.ID)), slog.String("source_id", it.SourceId), slog.String("error", err.Error()))
				continue
			}
			if err := s.rdb.HSet(ctx, dedupKey, it.SourceId, newPrice).Err(); err != nil {
				s.logger.Error("redis hset failed for new item", slog.String("task_id", intToString(task.ID)), slog.String("source_id", it.SourceId), slog.String("error", err.Error()))
			}

			s.linkTaskItem(task.ID, itemID, idx+1, linkTime)

			// 仅当认为是“真正的新品”（linkTime == batchTime）时才通知
			realNew := linkTime.Equal(batchTime)

			if realNew && notify && s.notifier != nil && userEmail != "" {
				item := &model.Item{
					SourceID: it.SourceId,
					Title:    it.Title,
					Price:    it.Price,
					ImageURL: it.ImageUrl,
					ItemURL:  it.ItemUrl,
				}
				if err := s.notifier.Send(ctx, item, "Found New Item", task.Keyword, 0, userEmail); err != nil {
					s.logger.Warn("send new item notification failed", slog.String("task_id", intToString(task.ID)), slog.String("source_id", it.SourceId), slog.String("error", err.Error()))
				}
			}
			if notify && realNew {
				s.logger.Info("Found New Item", slog.String("task_id", intToString(task.ID)), slog.String("source_id", it.SourceId), slog.Int("price", int(newPrice)))
			} else {
				s.logger.Debug("item ingested", slog.String("task_id", intToString(task.ID)), slog.String("source_id", it.SourceId), slog.Bool("real_new", realNew))
			}
			continue
		}

		// 2. 处理旧商品
		oldPrice, convErr := atoi32(oldPriceStr)
		if convErr != nil {
			s.logger.Warn("invalid cached price", slog.String("task_id", intToString(task.ID)), slog.String("source_id", it.SourceId), slog.String("error", convErr.Error()))
		}

		// 优化：价格未变，则跳过 DB 更新
		if newPrice == oldPrice {
			s.logger.Debug("skip unchanged item db write", slog.String("task_id", intToString(task.ID)), slog.String("source_id", it.SourceId))
			var item model.Item
			// 仅查询 ID 用于关联
			if err := s.db.Model(&model.Item{}).Select("id").Where("source_id = ?", it.SourceId).First(&item).Error; err != nil {
				s.logger.Error("failed to get id for existing item", slog.String("task_id", intToString(task.ID)), slog.String("source_id", it.SourceId), slog.String("error", err.Error()))
				continue
			}
			s.linkTaskItem(task.ID, item.ID, idx+1, batchTime) // linkTaskItem 是幂等的，这里的时间实际上不会覆盖旧时间
			continue
		}

		// 价格有变，执行 Upsert
		itemID, err := s.upsertItem(ctx, it)
		if err != nil {
			s.logger.Error("update item failed", slog.String("task_id", intToString(task.ID)), slog.String("source_id", it.SourceId), slog.String("error", err.Error()))
			continue
		}
		s.linkTaskItem(task.ID, itemID, idx+1, batchTime) // 同上

		// 更新 Redis 价格
		if err := s.rdb.HSet(ctx, dedupKey, it.SourceId, newPrice).Err(); err != nil {
			s.logger.Error("redis hset failed for updated item", slog.String("task_id", intToString(task.ID)), slog.String("source_id", it.SourceId), slog.String("error", err.Error()))
		}

		// 如果是降价，特别记录
		if newPrice < oldPrice {
			// 降价时刷新 Task-Item 关联时间，用于时间轴与 NEW 标记
			if err := s.db.Model(&model.TaskItem{}).
				Where("task_id = ? AND item_id = ?", task.ID, itemID).
				Update("created_at", time.Now()).Error; err != nil {
				s.logger.Warn("touch task_item failed", slog.String("task_id", intToString(task.ID)), slog.String("item_id", intToString(itemID)), slog.String("error", err.Error()))
			}
			if notify && s.notifier != nil && userEmail != "" {
				item := &model.Item{
					SourceID: it.SourceId,
					Title:    it.Title,
					Price:    it.Price,
					ImageURL: it.ImageUrl,
					ItemURL:  it.ItemUrl,
				}
				if err := s.notifier.Send(ctx, item, "Price Drop Detected", task.Keyword, oldPrice, userEmail); err != nil {
					s.logger.Warn("send price drop notification failed", slog.String("task_id", intToString(task.ID)), slog.String("source_id", it.SourceId), slog.String("error", err.Error()))
				}
			}
			if notify {
				s.logger.Info("Price Drop Detected", slog.String("task_id", intToString(task.ID)), slog.String("source_id", it.SourceId), slog.Int("old_price", int(oldPrice)), slog.Int("new_price", int(newPrice)))
			}
		} else {
			s.logger.Debug("item price updated", slog.String("task_id", intToString(task.ID)), slog.String("source_id", it.SourceId), slog.Int("old_price", int(oldPrice)), slog.Int("new_price", int(newPrice)))
		}
	}

	// 任务清理：删除旧的 task_items
	s.cleanupOldItems(ctx, task.ID)
}

// cleanupOldItems 移除超出最大限制的旧商品关联。
//
// 策略：
// 1. 检查 task_items 总数。
// 2. 如果超过 maxItemsPerTask，则查找第 N+1 个最新的创建时间。
// 3. 删除所有创建时间早于该时间的记录。
func (s *Scheduler) cleanupOldItems(ctx context.Context, taskID uint) {
	if s.maxItemsPerTask <= 0 {
		return
	}

	var count int64
	if err := s.db.WithContext(ctx).Model(&model.TaskItem{}).Where("task_id = ?", taskID).Count(&count).Error; err != nil {
		s.logger.Error("count task_items failed", slog.String("task_id", intToString(taskID)), slog.String("error", err.Error()))
		return
	}

	if count <= int64(s.maxItemsPerTask) {
		return
	}

	// 查找第 maxItemsPerTask 个记录的创建时间 (按倒序)
	// 例如保留 300，则查找第 300 个记录（OFFSET 299 LIMIT 1）的时间
	var cutoffTime time.Time
	err := s.db.WithContext(ctx).Model(&model.TaskItem{}).
		Select("created_at").
		Where("task_id = ?", taskID).
		Order("created_at DESC, `rank` ASC").
		Offset(s.maxItemsPerTask).
		Limit(1).
		Scan(&cutoffTime).Error

	if err != nil {
		s.logger.Error("find cleanup cutoff time failed", slog.String("task_id", intToString(taskID)), slog.String("error", err.Error()))
		return
	}

	if cutoffTime.IsZero() {
		return
	}

	// 删除所有早于等于 cutoffTime 的记录
	// 注意：可能存在微秒级相等的情况，但这对于“旧数据堆积”的清理是可以接受的
	// 我们使用 created_at < cutoffTime 来保留最新的 maxItemsPerTask 个
	// 但实际上是 DELETE ... WHERE created_at <= cutoffTime
	// 为了精确，我们可以再加一个缓冲区，或者简单删除
	res := s.db.WithContext(ctx).Where("task_id = ? AND created_at <= ?", taskID, cutoffTime).Delete(&model.TaskItem{})
	if res.Error != nil {
		s.logger.Error("cleanup old items failed", slog.String("task_id", intToString(taskID)), slog.String("error", res.Error.Error()))
	} else {
		s.logger.Info("cleaned up old items", slog.String("task_id", intToString(taskID)), slog.Int64("deleted", res.RowsAffected))
	}
}

// processItemsGuest 在游客模式下仅更新 Redis，不写入 MySQL。
//
//lint:ignore U1000 Temporary unused
func (s *Scheduler) processItemsGuest(task *model.Task, items []*pb.Item) {
	ctx := context.Background()
	dedupKey := "dedup:" + intToString(task.ID)
	cacheKey := "guest:task:" + intToString(task.ID) + ":items"

	type guestItem struct {
		ID        string    `json:"id"`
		SourceID  string    `json:"source_id"`
		Title     string    `json:"title"`
		Price     int32     `json:"price"`
		ImageURL  string    `json:"image_url"`
		ItemURL   string    `json:"item_url"`
		CreatedAt time.Time `json:"created_at"`
		IsNew     bool      `json:"is_new"`
	}

	existingItems := map[string]guestItem{}
	if raw, err := s.rdb.Get(ctx, cacheKey).Result(); err == nil && raw != "" {
		var cached []guestItem
		if jsonErr := json.Unmarshal([]byte(raw), &cached); jsonErr == nil {
			for _, it := range cached {
				if it.SourceID != "" {
					existingItems[it.SourceID] = it
				}
			}
		}
	}

	now := time.Now()
	result := make([]guestItem, 0, len(items))
	for i, it := range items {
		if strings.TrimSpace(it.SourceId) == "" {
			continue
		}
		oldPriceStr, err := s.rdb.HGet(ctx, dedupKey, it.SourceId).Result()
		if err != nil && err != redis.Nil {
			continue
		}
		isNew := err == redis.Nil
		newPrice := it.Price
		isNewFlag := false
		createdAt := now

		if isNew {
			_ = s.rdb.HSet(ctx, dedupKey, it.SourceId, newPrice).Err()
			isNewFlag = true
		} else {
			oldPrice, convErr := atoi32(oldPriceStr)
			if convErr != nil {
				continue
			}
			if newPrice < oldPrice {
				_ = s.rdb.HSet(ctx, dedupKey, it.SourceId, newPrice).Err()
				isNewFlag = true
			}
		}

		if cached, ok := existingItems[it.SourceId]; ok && !isNewFlag {
			createdAt = cached.CreatedAt
		}

		// 计算 IsNew 状态（保持与 DB 模式一致的逻辑）
		// 1. 必须在基准时间之后发现（BaselineAt）
		// 2. 发现时间距今不超过配置的 newItemDuration
		isNewStatus := false

		if task.BaselineAt != nil && !task.BaselineAt.IsZero() {
			if createdAt.After(*task.BaselineAt) {
				if now.Sub(createdAt) <= s.newItemDuration {
					isNewStatus = true
				}
			}
		} else {
			// 如果没有 BaselineAt (首次运行)，所有商品都不算 New
			isNewStatus = false
		}

		// 修复：防止因抓取深度增加导致的"虚假旧货变新货"
		// 规则：只有在列表中排名前 30 的商品，才允许因"新发现"而被强制标记为 New。
		// 超过 30 名之后的"新发现"，大概率是旧货，不予标记。
		if isNewFlag && task.BaselineAt != nil && !task.BaselineAt.IsZero() {
			if i < 30 {
				isNewStatus = true
			} else {
				// 如果位置靠后，但我们要保留它是"新加入"的事实以便缓存时间正确，
				// 但不给它 IsNew 的 UI 状态
				// 在这里我们重置 createdAt 为稍早一点的时间，或者干脆只把 UI 状态置为 false
				isNewStatus = false
			}
		}

		result = append(result, guestItem{
			ID:        it.SourceId,
			SourceID:  it.SourceId,
			Title:     it.Title,
			Price:     it.Price,
			ImageURL:  it.ImageUrl,
			ItemURL:   it.ItemUrl,
			CreatedAt: createdAt,
			IsNew:     isNewStatus,
		})
	}

	if len(result) > 100 {
		result = result[:100]
	}
	if payload, err := json.Marshal(result); err == nil {
		_ = s.rdb.Set(ctx, cacheKey, payload, 10*time.Minute).Err()
	}
}

// upsertItem 使用 gorm's OnConflict (Upsert) 功能确保商品存在并更新基础信息，返回 itemID。
//
// 这是一个原子操作，可以避免在并发环境下的竞态条件。
// 注意：这要求数据库的 `items` 表在 `source_id` 字段上有一个唯一索引 (UNIQUE INDEX)。
func (s *Scheduler) upsertItem(ctx context.Context, it *pb.Item) (uint, error) {
	item := model.Item{
		SourceID: it.SourceId,
		Title:    it.Title,
		Price:    it.Price,
		ImageURL: it.ImageUrl,
		ItemURL:  it.ItemUrl,
	}

	// 使用 INSERT ... ON DUPLICATE KEY UPDATE 实现原子化 Upsert
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "source_id"}},                                          // 冲突检测列
		DoUpdates: clause.AssignmentColumns([]string{"title", "price", "image_url", "item_url"}), // 冲突时更新这些列
	}).Create(&item).Error; err != nil {
		return 0, err
	}

	// 某些驱动在冲突更新时不会回填 ID，这里做一次兜底查询。
	if item.ID == 0 {
		var existing model.Item
		if err := s.db.WithContext(ctx).Select("id").Where("source_id = ?", it.SourceId).First(&existing).Error; err != nil {
			return 0, err
		}
		item.ID = existing.ID
	}

	return item.ID, nil
}

// linkTaskItem 创建 Task 与 Item 的关联（幂等）。
func (s *Scheduler) linkTaskItem(taskID, itemID uint, rank int, linkedAt time.Time) {
	if err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "task_id"}, {Name: "item_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"rank"}),
	}).Create(&model.TaskItem{
		TaskID:    taskID,
		ItemID:    itemID,
		Rank:      rank,
		CreatedAt: linkedAt,
	}).Error; err != nil {
		s.logger.Warn("link task item failed", slog.String("task_id", intToString(taskID)), slog.String("item_id", intToString(itemID)), slog.String("error", err.Error()))
	}
}

func intToString(v uint) string {
	return strconv.FormatUint(uint64(v), 10)
}

func parseTaskID(v string) (uint, error) {
	if strings.TrimSpace(v) == "" {
		return 0, fmt.Errorf("task id is empty")
	}
	parsed, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, err
	}
	return uint(parsed), nil
}

func atoi32(s string) (int32, error) {
	i, err := strconv.Atoi(s)
	return int32(i), err
}

// printQueueStats 打印队列统计信息。
func (s *Scheduler) printQueueStats() {
	stats := s.queue.Stats()
	s.logger.Info("queue statistics",
		slog.Int("pending", s.queue.Len()),
		slog.Int("capacity", s.queue.Cap()),
		slog.Int64("total_enqueued", stats.TotalEnqueued),
		slog.Int64("total_processed", stats.TotalProcessed),
		slog.Int64("total_succeeded", stats.TotalSucceeded),
		slog.Int64("total_failed", stats.TotalFailed),
		slog.Int64("total_dropped", stats.TotalDropped),
		slog.Int64("total_panics", stats.TotalPanics),
	)

	// 告警：如果丢弃任务数过多，说明需要增加 worker 数量或队列容量
	if stats.TotalDropped > 100 {
		s.logger.Warn("high task drop rate detected, consider increasing workers or queue capacity",
			slog.Int64("total_dropped", stats.TotalDropped))
	}
}
