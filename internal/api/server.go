package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"goodshunter/internal/api/auth"
	"goodshunter/internal/api/middleware"
	"goodshunter/internal/api/scheduler"
	"goodshunter/internal/config"
	"goodshunter/internal/crawler"
	"goodshunter/internal/model"
	"goodshunter/internal/pkg/dedup"
	"goodshunter/internal/pkg/metrics"
	"goodshunter/internal/pkg/notify"
	"goodshunter/internal/pkg/redisqueue"
	"goodshunter/proto/pb"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormLogger "gorm.io/gorm/logger"
)

// Server 封装了 API 服务所需的依赖和路由处理。
//
// 它持有数据库连接、Redis 客户端、爬虫 gRPC 客户端以及 Gin 路由引擎。
type Server struct {
	cfg           *config.Config
	logger        *slog.Logger
	db            *gorm.DB
	rdb           *redis.Client
	router        *gin.Engine
	sched         *scheduler.Scheduler
	auth          *auth.Handler
	deduper       Deduper
	taskStore     TaskStore
	taskScheduler TaskScheduler
}

type TaskStore interface {
	CountTasks(ctx context.Context, userID int) (int64, error)
	CreateTask(ctx context.Context, task *model.Task) error
}

type TaskScheduler interface {
	StartTask(ctx context.Context, task *model.Task)
}

type Deduper interface {
	IsDuplicate(ctx context.Context, url string) (bool, error)
	Delete(ctx context.Context, url string) error
}

type dbTaskStore struct {
	db *gorm.DB
}

func (s dbTaskStore) CountTasks(ctx context.Context, userID int) (int64, error) {
	var taskCount int64
	if err := s.db.WithContext(ctx).Model(&model.Task{}).Where("user_id = ?", userID).Count(&taskCount).Error; err != nil {
		return 0, err
	}
	return taskCount, nil
}

func (s dbTaskStore) CreateTask(ctx context.Context, task *model.Task) error {
	return s.db.WithContext(ctx).Create(task).Error
}

// NewServer 初始化 API 服务器。
//
// 它负责：
// 1. 连接 MySQL 数据库并执行自动迁移
// 2. 连接 Redis
// 3. 连接爬虫 gRPC 服务
// 4. 初始化 Gin 路由引擎
//
// 参数:
//
//	ctx: 上下文
//	cfg: 配置对象
//	logger: 日志记录器
//
// 返回值:
//
//	*Server: 初始化完成的服务器实例
//	error: 初始化失败返回错误
func NewServer(ctx context.Context, cfg *config.Config, logger *slog.Logger, redisQueue *redisqueue.Client) (*Server, error) {
	db, err := gorm.Open(mysql.Open(cfg.MySQL.DSN), &gorm.Config{
		Logger: gormLogger.Default.LogMode(gormLogger.Silent), // 关闭GORM调试日志
	})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(&model.User{}, &model.Task{}, &model.Item{}, &model.TaskItem{}); err != nil {
		return nil, err
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       0,
	})
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, err
	}

	emailNotifier := notify.NewEmailNotifier(&cfg.Email, logger)

	// 创建调度器，使用配置中的 Worker Pool 参数
	sched := scheduler.NewScheduler(
		db,
		rdb,
		logger,
		redisQueue,
		emailNotifier,
		cfg.App.ScheduleInterval,
		cfg.App.NewItemDuration,
		cfg.App.MaxItemsPerTask,
		cfg.App.WorkerPoolSize, // Worker Pool 大小
		cfg.App.QueueCapacity,  // 队列容量
		cfg.App.QueueBatchSize,
	)

	deduper := dedup.NewDeduplicator(rdb, time.Duration(cfg.App.DedupWindow)*time.Second)

	// 初始化 Prometheus 指标
	metrics.InitMetrics(cfg.App.WorkerPoolSize)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.RequestLogger(logger))

	s := &Server{
		cfg:           cfg,
		logger:        logger,
		db:            db,
		rdb:           rdb,
		router:        r,
		sched:         sched,
		auth:          auth.NewHandler(db, cfg.Security.JWTSecret, cfg.Security.InviteCode, emailNotifier, logger),
		deduper:       deduper,
		taskStore:     dbTaskStore{db: db},
		taskScheduler: sched,
	}
	s.registerRoutes()
	return s, nil
}

// Run 启动 HTTP 服务器并开始监听请求。
//
// 参数:
//
//	无 (使用配置中的地址)
//
// 返回值:
//
//	error: 服务器运行出错时返回
func (s *Server) Run() error {
	s.StartScheduler(context.Background())

	s.logger.Info("api server listening", slog.String("addr", s.cfg.App.HTTPAddr))
	return s.router.Run(s.cfg.App.HTTPAddr)
}

// Router 返回 HTTP 路由处理器。
func (s *Server) Router() http.Handler {
	return s.router
}

// StartScheduler 启动调度器。
func (s *Server) StartScheduler(ctx context.Context) {
	// 1. 保护任务分发器
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.logger.Error("PANIC in scheduler dispatcher", slog.Any("panic", r))
				// 可选：在这里发送告警或尝试重启
			}
		}()
		s.sched.DispatchTasks(ctx)
	}()

	// 2. 保护结果监听器
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.logger.Error("PANIC in result listener", slog.Any("panic", r))
			}
		}()
		if err := s.sched.StartResultListener(ctx); err != nil {
			s.logger.Error("result listener stopped", slog.String("error", err.Error()))
		}
	}()
}

// Close 关闭数据库与缓存连接。
func (s *Server) Close() error {
	var firstErr error
	if s.rdb != nil {
		if err := s.rdb.Close(); err != nil {
			firstErr = err
		}
	}
	if s.db != nil {
		sqlDB, err := s.db.DB()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
		} else {
			if closeErr := sqlDB.Close(); closeErr != nil {
				if firstErr == nil {
					firstErr = closeErr
				}
			}
		}
	}
	return firstErr
}

// registerRoutes 注册所有的 API 路由。
func (s *Server) registerRoutes() {
	s.router.StaticFile("/", "./web/index.html")
	s.router.Static("/assets", "./web/assets")

	// Prometheus metrics 端点
	s.router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	s.router.GET("/healthz", s.handleHealthz)

	s.router.POST("/register", s.auth.Register)
	s.router.POST("/login", s.auth.Login)
	s.router.POST("/login/guest", s.auth.GuestLogin)
	s.router.POST("/verify", s.auth.VerifyEmail)
	s.router.POST("/resend", s.auth.ResendCode)

	authed := s.router.Group("/")
	authed.Use(middleware.AuthMiddleware(s.cfg.Security.JWTSecret))
	authed.Use(middleware.GuestActivityMiddleware(s.rdb, s.cfg.App.GuestIdleTimeout))
	authed.GET("/config", s.handleGetConfig)
	authed.POST("/logout", s.auth.Logout)
	authed.POST("/me/delete", s.handleDeleteAccount)
	authed.POST("/tasks", s.handleCreateTask)
	authed.GET("/tasks", s.handleListTasks)
	authed.PATCH("/tasks/:id", s.handleUpdateTask)
	authed.POST("/tasks/:id/status", s.handleUpdateTaskStatus)
	authed.PATCH("/tasks/:id/notify", s.handleToggleNotify)
	authed.DELETE("/tasks/:id", s.handleDeleteTask)
	authed.GET("/timeline", s.handleTimeline)
}

func (s *Server) handleHealthz(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	if s.db == nil || s.rdb == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error"})
		return
	}

	var one int
	if err := s.db.WithContext(ctx).Raw("SELECT 1").Scan(&one).Error; err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error"})
		return
	}
	if err := s.rdb.Ping(ctx).Err(); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// createTaskRequest 创建任务的请求参数。
type createTaskRequest struct {
	Keyword  string `json:"keyword" binding:"required"`
	MinPrice int32  `json:"min_price"`
	MaxPrice int32  `json:"max_price"`
	Platform int    `json:"platform"`
	Sort     string `json:"sort"` // 前端可能传字符串，如 "price"
	Status   string `json:"status"`
	Type     string `json:"type"` // monitor / oneshot
}

// createTaskResponse 创建任务的响应。
type createTaskResponse struct {
	ID uint `json:"id"`
}

type taskResponse struct {
	ID            uint   `json:"id"`
	UserID        uint   `json:"user_id"`
	Keyword       string `json:"keyword"`
	MinPrice      int32  `json:"min_price"`
	MaxPrice      int32  `json:"max_price"`
	Platform      int    `json:"platform"`
	SortBy        int    `json:"sort_by"`
	Status        string `json:"status"`
	NotifyEnabled bool   `json:"notify_enabled"`
	Type          string `json:"type" gorm:"column:task_type"`
}

type updateTaskStatusRequest struct {
	Status string `json:"status" binding:"required"`
}

type updateNotifyRequest struct {
	Enabled bool `json:"enabled"`
}

type updateTaskRequest struct {
	Keyword  *string `json:"keyword"`
	MinPrice *int32  `json:"min_price"`
	MaxPrice *int32  `json:"max_price"`
	Platform *int    `json:"platform"`
	Sort     *string `json:"sort"`
}

// handleDeleteTask 删除任务并清理关联。
func (s *Server) handleDeleteTask(c *gin.Context) {
	if getUserRole(c) == "guest" {
		c.JSON(http.StatusForbidden, gin.H{"error": "演示模式下禁止创建/删除任务"})
		return
	}
	id := c.Param("id")
	userID := getUserID(c)
	taskIDUint, err := strconv.ParseUint(id, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid task id"})
		return
	}

	var dedupURL string
	var task model.Task
	if err := s.db.Select("keyword", "min_price", "max_price", "platform", "sort_by", "sort_order").
		Where("id = ? AND user_id = ?", id, userID).
		First(&task).Error; err == nil {
		req := createTaskRequest{
			Keyword:  task.Keyword,
			MinPrice: task.MinPrice,
			MaxPrice: task.MaxPrice,
			Platform: task.Platform,
		}
		dedupURL = buildDedupURL(req, task.SortBy, task.SortOrder)
	}

	// 停止调度器
	s.sched.StopTask(uint(taskIDUint))

	// 1. 获取所有关联的商品ID
	var itemIDs []uint
	s.db.Model(&model.TaskItem{}).
		Joins("JOIN tasks ON tasks.id = task_items.task_id").
		Where("task_items.task_id = ? AND tasks.user_id = ?", id, userID).
		Pluck("item_id", &itemIDs)

	// 2. 删除task_items关联
	if err := s.db.Where("task_id = ? AND task_id IN (SELECT id FROM tasks WHERE user_id = ?)", id, userID).Delete(&model.TaskItem{}).Error; err != nil {
		s.logger.Warn("delete task_items failed", slog.String("error", err.Error()))
	}

	// 3. 删除items（只有该任务独占的商品）
	if len(itemIDs) > 0 {
		// 查找没有被其他任务引用的商品
		var orphanedItemIDs []uint
		s.db.Table("items").
			Select("items.id").
			Where("items.id IN ?", itemIDs).
			Not("items.id IN (?)", s.db.Table("task_items").Select("item_id")).
			Pluck("id", &orphanedItemIDs)

		if len(orphanedItemIDs) > 0 {
			if err := s.db.Where("id IN ?", orphanedItemIDs).Delete(&model.Item{}).Error; err != nil {
				s.logger.Warn("delete orphaned items failed", slog.String("error", err.Error()))
			} else {
				s.logger.Info("deleted orphaned items", slog.Int("count", len(orphanedItemIDs)))
			}
		}
	}

	// 4. 删除任务本身
	if err := s.db.Where("id = ? AND user_id = ?", id, userID).Delete(&model.Task{}).Error; err != nil {
		s.logger.Error("delete task failed", slog.String("error", err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete task failed"})
		return
	}
	if dedupURL != "" {
		if err := s.deduper.Delete(c.Request.Context(), dedupURL); err != nil {
			s.logger.Warn("dedup delete failed", slog.String("error", err.Error()), slog.String("url", dedupURL))
		}
	}

	c.JSON(http.StatusOK, gin.H{"deleted": id})
}

// handleDeleteAccount 注销账户并清理关联数据。
func (s *Server) handleDeleteAccount(c *gin.Context) {
	userID := getUserID(c)
	if getUserRole(c) == "guest" {
		c.JSON(http.StatusForbidden, gin.H{"error": "演示账号不可注销"})
		return
	}

	tx := s.db.Begin()
	if tx.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "transaction failed"})
		return
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	var taskIDs []uint
	if err := tx.Model(&model.Task{}).Where("user_id = ?", userID).Pluck("id", &taskIDs).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load tasks failed"})
		return
	}

	for _, id := range taskIDs {
		s.sched.StopTask(id)
	}

	var itemIDs []uint
	if len(taskIDs) > 0 {
		if err := tx.Model(&model.TaskItem{}).
			Where("task_id IN ?", taskIDs).
			Pluck("item_id", &itemIDs).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "load items failed"})
			return
		}

		if err := tx.Where("task_id IN ?", taskIDs).Delete(&model.TaskItem{}).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "delete task_items failed"})
			return
		}
	}

	if len(itemIDs) > 0 {
		var orphanedItemIDs []uint
		if err := tx.Table("items").
			Select("items.id").
			Where("items.id IN ?", itemIDs).
			Not("items.id IN (?)", tx.Table("task_items").Select("item_id")).
			Pluck("id", &orphanedItemIDs).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "load orphaned items failed"})
			return
		}
		if len(orphanedItemIDs) > 0 {
			if err := tx.Where("id IN ?", orphanedItemIDs).Delete(&model.Item{}).Error; err != nil {
				tx.Rollback()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "delete items failed"})
				return
			}
		}
	}

	if err := tx.Where("user_id = ?", userID).Delete(&model.Task{}).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete tasks failed"})
		return
	}

	if err := tx.Where("id = ?", userID).Delete(&model.User{}).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete user failed"})
		return
	}

	if err := tx.Commit().Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "commit failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// handleListTasks 返回任务列表，用于前端展示。
func (s *Server) handleListTasks(c *gin.Context) {
	tasks := []taskResponse{} // Initialize as empty slice to ensure JSON is [] not null
	userID := getUserID(c)
	if err := s.db.Model(&model.Task{}).
		Select("id, user_id, keyword, min_price, max_price, platform, sort_by, status, notify_enabled, task_type").
		Where("user_id = ?", userID).
		Order("id DESC").
		Scan(&tasks).Error; err != nil {
		s.logger.Error("list tasks failed", slog.String("error", err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list tasks failed"})
		return
	}
	c.JSON(http.StatusOK, tasks)
}

// handleToggleNotify 更新任务通知开关。
//
// PATCH /tasks/:id/notify
func (s *Server) handleToggleNotify(c *gin.Context) {
	id := c.Param("id")
	userID := getUserID(c)
	var req updateNotifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := s.db.Model(&model.Task{}).
		Where("id = ? AND user_id = ?", id, userID).
		Update("notify_enabled", req.Enabled).Error; err != nil {
		s.logger.Error("update notify failed", slog.String("error", err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update notify failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"enabled": req.Enabled})
}

func (s *Server) handleUpdateTaskStatus(c *gin.Context) {
	id := c.Param("id")
	userID := getUserID(c)
	var req updateTaskStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Status != "running" && req.Status != "stopped" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid status"})
		return
	}

	if err := s.db.Model(&model.Task{}).Where("id = ? AND user_id = ?", id, userID).Update("status", req.Status).Error; err != nil {
		s.logger.Error("update task status failed", slog.String("error", err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
		return
	}

	// 根据新状态启停调度器
	taskIDUint, _ := strconv.ParseUint(id, 10, 32)
	if req.Status == "running" {
		var task model.Task
		if err := s.db.Where("id = ? AND user_id = ?", taskIDUint, userID).First(&task).Error; err == nil {
			s.sched.StartTask(context.Background(), &task)
		}
	} else {
		s.sched.StopTask(uint(taskIDUint))
	}

	c.JSON(http.StatusOK, gin.H{"status": req.Status})
}

func (s *Server) handleUpdateTask(c *gin.Context) {
	if getUserRole(c) == "guest" {
		c.JSON(http.StatusForbidden, gin.H{"error": "演示模式下禁止修改任务"})
		return
	}
	id := c.Param("id")
	userID := getUserID(c)

	var req updateTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	updates := map[string]interface{}{}
	if req.Keyword != nil {
		keyword := strings.TrimSpace(*req.Keyword)
		if keyword == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid keyword"})
			return
		}
		updates["keyword"] = keyword
	}
	if req.MinPrice != nil {
		if *req.MinPrice < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid min_price"})
			return
		}
		updates["min_price"] = *req.MinPrice
	}
	if req.MaxPrice != nil {
		if *req.MaxPrice < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid max_price"})
			return
		}
		updates["max_price"] = *req.MaxPrice
	}
	if req.Platform != nil {
		if *req.Platform <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid platform"})
			return
		}
		updates["platform"] = *req.Platform
	}
	if req.Sort != nil {
		sortBy, sortOrder := mapSortAndOrder(*req.Sort)
		if sortBy == -1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid sort"})
			return
		}
		updates["sort_by"] = sortBy
		updates["sort_order"] = sortOrder
	}

	if len(updates) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no updates"})
		return
	}

	if err := s.db.Model(&model.Task{}).Where("id = ? AND user_id = ?", id, userID).Updates(updates).Error; err != nil {
		s.logger.Error("update task failed", slog.String("error", err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"updated": true})
}

// handleCreateTask 处理创建监控任务的请求。
//
// POST /tasks
func (s *Server) handleCreateTask(c *gin.Context) {
	if getUserRole(c) == "guest" {
		c.JSON(http.StatusForbidden, gin.H{"error": "演示模式下禁止创建/删除任务"})
		return
	}
	var req createTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	userID := getUserID(c)

	taskCount, err := s.taskStore.CountTasks(c.Request.Context(), userID)
	if err != nil {
		s.logger.Error("count tasks failed", slog.String("error", err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "count tasks failed"})
		return
	}
	maxTasks := s.cfg.App.MaxTasksPerUser
	if maxTasks <= 0 {
		maxTasks = 3
	}
	if taskCount >= int64(maxTasks) {
		c.JSON(http.StatusForbidden, gin.H{"error": "task limit reached"})
		return
	}

	sortBy, sortOrder := mapSortAndOrder(req.Sort)
	if sortBy == -1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid sort"})
		return
	}

	dedupURL := buildDedupURL(req, sortBy, sortOrder)
	if dedupURL != "" {
		dup, err := s.deduper.IsDuplicate(c.Request.Context(), dedupURL)
		if err != nil {
			s.logger.Error("dedup check failed", slog.String("error", err.Error()), slog.String("url", dedupURL))
		} else if dup {
			s.logger.Info("task deduplicated", slog.String("url", dedupURL))
			metrics.TaskDuplicatePreventedTotal.Inc()
			c.JSON(http.StatusOK, gin.H{"status": "skipped_duplicate"})
			return
		}
	}

	status := req.Status
	if status == "" {
		status = "running"
	}
	taskType := strings.ToLower(strings.TrimSpace(req.Type))
	if taskType == "" {
		taskType = string(model.TaskTypeMonitor)
	}
	switch taskType {
	case string(model.TaskTypeMonitor), string(model.TaskTypeOneShot):
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid task type"})
		return
	}

	task := model.Task{
		UserID:    uint(userID),
		Keyword:   req.Keyword,
		MinPrice:  req.MinPrice,
		MaxPrice:  req.MaxPrice,
		Platform:  req.Platform,
		SortBy:    sortBy,
		SortOrder: sortOrder,
		Status:    status,
		Type:      model.TaskType(taskType),
	}

	if err := s.taskStore.CreateTask(c.Request.Context(), &task); err != nil {
		s.logger.Error("create task failed", slog.String("error", err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create task failed"})
		return
	}

	// 如果任务状态为 running，发布到队列
	if task.Status == "running" {
		s.taskScheduler.StartTask(context.Background(), &task)
	}

	c.JSON(http.StatusCreated, createTaskResponse{ID: task.ID})
}

// mapSortString 将前端传递的排序字符串映射为 Protobuf 枚举值。
//
// 支持的字符串: "created_time", "price", "score", "num_likes" 等。
//
// 参数:
//
//	s: 排序字符串
//
// 返回值:
//
//	int: 对应的枚举整数值，如果无效则返回 -1
func mapSortAndOrder(s string) (int, int) {
	parts := strings.Split(s, "|")
	sortStr := parts[0]
	orderStr := ""
	if len(parts) > 1 {
		orderStr = parts[1]
	}

	var sortBy int
	switch strings.ToLower(strings.TrimSpace(sortStr)) {
	case "", "created_time", "created", "time":
		sortBy = int(pb.SortBy_SORT_BY_CREATED_TIME)
	case "price":
		sortBy = int(pb.SortBy_SORT_BY_PRICE)
	case "score":
		sortBy = int(pb.SortBy_SORT_BY_SCORE)
	case "likes", "num_likes":
		sortBy = int(pb.SortBy_SORT_BY_NUM_LIKES)
	default:
		return -1, 0
	}

	sortOrder := int(pb.SortOrder_SORT_ORDER_DESC)
	if strings.ToLower(strings.TrimSpace(orderStr)) == "asc" {
		sortOrder = int(pb.SortOrder_SORT_ORDER_ASC)
	}

	return sortBy, sortOrder
}

func buildDedupURL(req createTaskRequest, sortBy, sortOrder int) string {
	if req.Keyword == "" {
		return ""
	}

	if req.Platform == int(pb.Platform_PLATFORM_MERCARI) {
		fetchReq := &pb.FetchRequest{
			Platform:   pb.Platform(req.Platform),
			Keyword:    req.Keyword,
			MinPrice:   req.MinPrice,
			MaxPrice:   req.MaxPrice,
			Sort:       pb.SortBy(sortBy),
			Order:      pb.SortOrder(sortOrder),
			OnlyOnSale: true,
		}
		return crawler.BuildMercariURL(fetchReq)
	}

	values := url.Values{}
	values.Set("keyword", req.Keyword)
	if req.MinPrice > 0 {
		values.Set("min_price", strconv.FormatInt(int64(req.MinPrice), 10))
	}
	if req.MaxPrice > 0 {
		values.Set("max_price", strconv.FormatInt(int64(req.MaxPrice), 10))
	}
	values.Set("sort_by", strconv.Itoa(sortBy))
	values.Set("sort_order", strconv.Itoa(sortOrder))

	u := url.URL{
		Scheme:   "goodshunter",
		Host:     fmt.Sprintf("platform-%d", req.Platform),
		Path:     "/search",
		RawQuery: values.Encode(),
	}
	return u.String()
}

// timelineItem 时间轴接口返回的商品结构。
type timelineItem struct {
	ID        string    `json:"id"`
	SourceID  string    `json:"source_id"`
	Title     string    `json:"title"`
	Price     int32     `json:"price"`
	ImageURL  string    `json:"image_url"`
	ItemURL   string    `json:"item_url"`
	CreatedAt time.Time `json:"created_at"`
	IsNew     bool      `json:"is_new"`
}

type timelineRow struct {
	ID                uint       `gorm:"column:id"`
	SourceID          string     `gorm:"column:source_id"`
	Title             string     `gorm:"column:title"`
	Price             int32      `gorm:"column:price"`
	ImageURL          string     `gorm:"column:image_url"`
	ItemURL           string     `gorm:"column:item_url"`
	CreatedAt         time.Time  `gorm:"column:created_at"`
	TaskItemCreatedAt time.Time  `gorm:"column:task_item_created_at"`
	BaselineAt        *time.Time `gorm:"column:baseline_at"`
}

func (s *Server) handleGetConfig(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"new_item_duration_ms": s.cfg.App.NewItemDuration.Milliseconds(),
		"guest_heartbeat_ms":   s.cfg.App.GuestHeartbeat.Milliseconds(),
		"max_tasks_per_user":   s.cfg.App.MaxTasksPerUser,
	})
}

// handleTimeline 处理获取商品时间轴的请求。
//
// GET /timeline?limit=20&offset=0
func (s *Server) handleTimeline(c *gin.Context) {
	userID := getUserID(c)
	// 即使是 Guest 也走统一的 DB 查询逻辑
	limit := parseQueryInt(c, "limit", 20)
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	offset := parseQueryInt(c, "offset", 0)
	taskID := c.Query("task_id")

	rows := []timelineRow{} // Initialize as empty slice
	query := s.db.Table("items").
		Select("items.id, items.source_id, items.title, items.price, items.image_url, items.item_url, items.created_at, task_items.created_at as task_item_created_at, tasks.baseline_at").
		Joins("JOIN task_items ON task_items.item_id = items.id").
		Joins("JOIN tasks ON tasks.id = task_items.task_id").
		Where("tasks.user_id = ?", userID)

	if taskID != "" {
		query = query.
			Where("task_items.task_id = ?", taskID).
			Order("task_items.created_at DESC, task_items.rank ASC")
	} else {
		query = query.Order("task_items.created_at DESC, task_items.rank ASC")
	}

	if err := query.Limit(limit).Offset(offset).Scan(&rows).Error; err != nil {
		s.logger.Error("query timeline failed", slog.String("error", err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "query timeline failed"})
		return
	}

	items := make([]timelineItem, 0, len(rows))
	now := time.Now()
	for _, row := range rows {
		isNew := false
		if row.BaselineAt != nil && !row.BaselineAt.IsZero() {
			if row.TaskItemCreatedAt.After(*row.BaselineAt) {
				diff := now.Sub(row.TaskItemCreatedAt)
				// 新商品有效期使用配置值
				if diff >= 0 && diff <= s.cfg.App.NewItemDuration {
					isNew = true
				}
			}
		}
		items = append(items, timelineItem{
			ID:        strconv.Itoa(int(row.ID)),
			SourceID:  row.SourceID,
			Title:     row.Title,
			Price:     row.Price,
			ImageURL:  row.ImageURL,
			ItemURL:   toFullMercariURL(row.ItemURL),
			CreatedAt: row.CreatedAt,
			IsNew:     isNew,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"items":   items,
		"status":  s.loadTaskCrawlStatus(c, taskID),
		"message": s.loadTaskCrawlMessage(c, taskID),
	})
}

//lint:ignore U1000 Temporary unused
func (s *Server) handleTimelineGuest(c *gin.Context) {
	taskID := c.Query("task_id")
	if taskID == "" {
		c.JSON(http.StatusOK, gin.H{"items": []timelineItem{}})
		return
	}
	cacheKey := "guest:task:" + taskID + ":items"
	raw, err := s.rdb.Get(c, cacheKey).Result()
	if err != nil || raw == "" {
		c.JSON(http.StatusOK, gin.H{
			"items":   []timelineItem{},
			"status":  s.loadTaskCrawlStatus(c, taskID),
			"message": s.loadTaskCrawlMessage(c, taskID),
		})
		return
	}

	var items []timelineItem
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		c.JSON(http.StatusOK, gin.H{"items": []timelineItem{}})
		return
	}
	for i := range items {
		items[i].ItemURL = toFullMercariURL(items[i].ItemURL)
	}
	c.JSON(http.StatusOK, gin.H{
		"items":   items,
		"status":  s.loadTaskCrawlStatus(c, taskID),
		"message": s.loadTaskCrawlMessage(c, taskID),
	})
}

func (s *Server) loadTaskCrawlStatus(ctx context.Context, taskID string) string {
	if taskID == "" {
		return ""
	}
	key := "task:crawl_status:" + taskID
	status, err := s.rdb.Get(ctx, key).Result()
	if err != nil {
		return ""
	}
	return status
}

func (s *Server) loadTaskCrawlMessage(ctx context.Context, taskID string) string {
	if taskID == "" {
		return ""
	}
	key := "task:crawl_message:" + taskID
	message, err := s.rdb.Get(ctx, key).Result()
	if err != nil {
		return ""
	}
	return message
}

// toFullMercariURL 将相对URL转换为完整的Mercari URL。
//
// 参数:
//
//	url: 原始URL（可能是相对路径）
//
// 返回值:
//
//	string: 完整的Mercari URL
func toFullMercariURL(url string) string {
	if url == "" {
		return ""
	}
	// 如果已经是完整URL，直接返回
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		return url
	}
	// 确保URL以/开头
	if !strings.HasPrefix(url, "/") {
		url = "/" + url
	}
	return "https://jp.mercari.com" + url
}

// parseQueryInt 解析查询参数中的整数值。
//
// 参数:
//
//	c: Gin 上下文
//	key: 参数名
//	def: 默认值
//
// 返回值:
//
//	int: 解析后的整数或默认值
func parseQueryInt(c *gin.Context, key string, def int) int {
	val := c.Query(key)
	if val == "" {
		return def
	}
	iv, err := strconv.Atoi(val)
	if err != nil {
		return def
	}
	return iv
}

func getUserID(c *gin.Context) int {
	return c.GetInt("userID")
}

func getUserRole(c *gin.Context) string {
	role, ok := c.Get("role")
	if !ok {
		return "admin"
	}
	if s, ok := role.(string); ok && s != "" {
		return s
	}
	return "admin"
}
