package crawler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"goodshunter/internal/config"
	"goodshunter/internal/pkg/metrics"
	"goodshunter/internal/pkg/ratelimit"
	"goodshunter/internal/pkg/redisqueue"
	"goodshunter/proto/pb"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
	"github.com/redis/go-redis/v9"
)

var (
	priceRe             = regexp.MustCompile(`[0-9]+`)
	priceWithCurrencyRe = regexp.MustCompile(`[¥￥]\s*([0-9][0-9,]*)`)
)

const (
	rateLimitKey     = "goodshunter:ratelimit:global"
	proxyCooldownKey = "goodshunter:proxy:cooldown"
	proxyCacheTTL    = 5 * time.Second
)

// Service 负责浏览器调度与页面解析。
//
// 它维护了一个 rod.Browser 实例，并发控制由 StartWorker 中的信号量管理。
type Service struct {
	browser         *rod.Browser
	rdb             *redis.Client
	rateLimiter     *ratelimit.RateLimiter
	logger          *slog.Logger
	defaultUA       string
	pageTimeout     time.Duration
	maxFetchCount   int
	cfg             *config.Config
	currentIsProxy  bool
	proxyCache      bool
	proxyCacheUntil time.Time
	mu              sync.RWMutex
	forceProxyOnce  uint32
	taskCounter     atomic.Uint64 // 用于触发 maxTasks 重启
	maxTasks        uint64
	restartCh       chan struct{}
	redisQueue      *redisqueue.Client

	// 后台任务控制
	bgCtx    context.Context
	bgCancel context.CancelFunc

	// 统计信息
	stats crawlerStats
}

// crawlerStats 爬虫统计信息
type crawlerStats struct {
	TotalProcessed atomic.Int64
	TotalSucceeded atomic.Int64
	TotalFailed    atomic.Int64
	TotalPanics    atomic.Int64
}

// NewService 启动浏览器实例并创建服务。
//
// 参数:
//
//	ctx: 上下文
//	cfg: 配置对象，包含浏览器路径、并发数等设置
//	logger: 日志记录器
//
// 返回值:
//
//	*Service: 初始化完成的服务实例
//	error: 如果浏览器启动失败则返回错误
func NewService(ctx context.Context, cfg *config.Config, logger *slog.Logger, redisQueue *redisqueue.Client) (*Service, error) {
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	browser, err := startBrowser(initCtx, cfg, logger, false)
	if err != nil {
		return nil, err
	}
	metrics.CrawlerBrowserInstances.Inc()

	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
	})
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("connect redis: %w", err)
	}

	var limiter *ratelimit.RateLimiter
	if cfg.App.RateLimit > 0 && cfg.App.RateBurst > 0 {
		limiter = ratelimit.NewRedisRateLimiter(rdb)
		logger.Info("rate limiter enabled",
			slog.Float64("rate", cfg.App.RateLimit),
			slog.Float64("burst", cfg.App.RateBurst))
	}

	logger.Info("crawler service initialized",
		slog.Int("max_concurrency", cfg.Browser.MaxConcurrency))

	forceProxyOnce := uint32(0)
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("FORCE_PROXY_ONCE"))); v == "1" || v == "true" || v == "yes" {
		forceProxyOnce = 1
		logger.Warn("force proxy switch enabled for next crawl", slog.String("env", "FORCE_PROXY_ONCE"))
	}

	maxTasks := uint64(0)
	if cfg.App.MaxTasks > 0 {
		maxTasks = uint64(cfg.App.MaxTasks)
	}

	pageTimeout := cfg.Browser.PageTimeout
	if pageTimeout <= 0 {
		pageTimeout = 60 * time.Second
	}

	// 创建后台任务的独立 context（由 Shutdown 控制生命周期）
	bgCtx, bgCancel := context.WithCancel(context.Background())

	service := &Service{
		browser:        browser,
		rdb:            rdb,
		rateLimiter:    limiter,
		logger:         logger,
		defaultUA:      "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/118.0.0.0 Safari/537.36",
		pageTimeout:    pageTimeout,
		maxFetchCount:  cfg.Browser.MaxFetchCount,
		cfg:            cfg,
		currentIsProxy: false,
		forceProxyOnce: forceProxyOnce,
		maxTasks:       maxTasks,
		restartCh:      make(chan struct{}, 1),
		redisQueue:     redisQueue,
		bgCtx:          bgCtx,
		bgCancel:       bgCancel,
	}
	metrics.CrawlerProxyMode.Set(0)

	// 启动后台任务（使用独立的 bgCtx，由 Shutdown 控制停止）
	go service.startBrowserHealthCheck(bgCtx)
	go service.startStuckTaskCleanup(bgCtx)

	return service, nil
}

// RestartSignal exposes the restart notification channel.
func (s *Service) RestartSignal() <-chan struct{} {
	return s.restartCh
}

// startBrowserHealthCheck 定期检查浏览器健康状态，如果无响应则重启浏览器实例。
func (s *Service) startBrowserHealthCheck(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second) // 每30秒检查一次
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !s.checkBrowserHealth(ctx) {
				s.logger.Warn("browser health check failed, restarting browser instance")
				if err := s.restartBrowserInstance(ctx); err != nil {
					s.logger.Error("failed to restart browser instance", slog.String("error", err.Error()))
				} else {
					s.logger.Info("browser instance restarted successfully")
				}
			}
		}
	}
}

// startStuckTaskCleanup 定期清理卡住的任务（超过 2 分钟的任务会被恢复）。
func (s *Service) startStuckTaskCleanup(ctx context.Context) {
	if s.redisQueue == nil {
		return
	}
	ticker := time.NewTicker(1 * time.Minute) // 每分钟检查一次
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rescueCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			count, err := s.redisQueue.RescueStuckTasks(rescueCtx, 2*time.Minute)
			cancel()
			if err != nil {
				s.logger.Warn("failed to rescue stuck tasks", slog.String("error", err.Error()))
			} else if count > 0 {
				s.logger.Info("rescued stuck tasks", slog.Int("count", count))
			}
		}
	}
}

// checkBrowserHealth 检查浏览器是否响应，返回 true 表示健康，false 表示无响应。
func (s *Service) checkBrowserHealth(ctx context.Context) bool {
	s.mu.RLock()
	browser := s.browser
	s.mu.RUnlock()

	if browser == nil {
		return false
	}

	// 尝试创建一个测试页面来检查浏览器是否响应
	healthCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	page, err := browser.Context(healthCtx).Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return false
	}
	defer func() {
		if page != nil {
			_ = page.Close()
		}
	}()

	// 尝试执行一个简单的 JavaScript 来验证浏览器响应
	_, err = page.Eval("() => document.title")
	return err == nil
}

// restartBrowserInstance 重启浏览器实例（保持当前的代理状态）。
func (s *Service) restartBrowserInstance(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 保存当前代理状态
	shouldUseProxy := s.currentIsProxy

	// 关闭旧浏览器
	if s.browser != nil {
		if err := s.browser.Close(); err != nil {
			s.logger.Warn("close old browser failed", slog.String("error", err.Error()))
		}
		s.browser = nil
	}

	// 启动新浏览器
	newBrowser, err := startBrowser(ctx, s.cfg, s.logger, shouldUseProxy)
	if err != nil {
		return fmt.Errorf("start new browser: %w", err)
	}

	s.browser = newBrowser
	mode := "direct"
	if shouldUseProxy {
		mode = "proxy"
	}
	s.logger.Info("browser instance restarted", slog.String("mode", mode))
	return nil
}

// StartWorker runs a Redis task consumption loop until ctx is canceled.
func (s *Service) StartWorker(ctx context.Context) error {
	if s.redisQueue == nil {
		return errors.New("redis queue client is not initialized")
	}

	// 令牌数 = 浏览器最大并发数，确保同时打开的页面数不超过配置值
	concurrencyLimit := s.cfg.Browser.MaxConcurrency
	if concurrencyLimit < 1 {
		concurrencyLimit = 1
	}
	sem := make(chan struct{}, concurrencyLimit)
	s.logger.Info("crawler worker started",
		slog.Int("max_concurrent_pages", concurrencyLimit))

	for {
		// 1. 在拉取任务前先申请令牌，如果处理不过来，就暂停拉取 Redis
		select {
		// 获取令牌
		case sem <- struct{}{}:
			// 成功获取令牌，继续
		case <-ctx.Done():
			return ctx.Err()
		}

		// 2. 拉取任务
		task, err := s.redisQueue.PopTask(ctx, 2*time.Second)
		if err != nil {
			<-sem // 拉取失败, 释放令牌
			if errors.Is(err, redisqueue.ErrNoTask) {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				s.logger.Info("worker loop stopped")
				return err
			}
			s.logger.Error("pop redis task failed", slog.String("error", err.Error()))
			time.Sleep(200 * time.Millisecond)
			continue
		}

		// 3. 处理任务（在独立 goroutine 中，带看门狗保护）
		go func(t *pb.FetchRequest) {
			taskID := t.GetTaskId()
			taskStart := time.Now()

			// 看门狗超时：确保无论如何都会释放信号量
			// 设置为 100 秒（比任务超时 90 秒稍长，给正常超时一点余量）
			watchdogTimeout := 100 * time.Second
			done := make(chan struct{})

			// 看门狗 goroutine
			go func() {
				select {
				case <-done:
					// 任务正常完成
				case <-time.After(watchdogTimeout):
					// 看门狗超时触发
					s.logger.Error("watchdog timeout triggered, task stuck",
						slog.String("task_id", taskID),
						slog.Duration("elapsed", time.Since(taskStart)))
					s.stats.TotalFailed.Add(1)
					metrics.CrawlerErrorsTotal.WithLabelValues("unknown", "watchdog_timeout").Inc()
				}
			}()

			// 确保信号量一定会被释放
			defer func() {
				close(done) // 通知看门狗任务已完成
				<-sem       // 释放令牌
				s.logger.Debug("task goroutine exited",
					slog.String("task_id", taskID),
					slog.Duration("total_duration", time.Since(taskStart)))
			}()

			// Panic 恢复
			defer func() {
				if r := recover(); r != nil {
					s.stats.TotalPanics.Add(1)
					s.logger.Error("crawl task panic recovered",
						slog.String("task_id", taskID),
						slog.Any("panic", r))
					// 推送错误响应
					resp := &pb.FetchResponse{
						ErrorMessage: fmt.Sprintf("panic: %v", r),
						TaskId:       taskID,
					}
					pushCtx, pushCancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer pushCancel()
					if pushErr := s.redisQueue.PushResult(pushCtx, resp); pushErr != nil {
						s.logger.Error("push panic result failed", slog.String("error", pushErr.Error()))
					}
				}
			}()

			// 为每个任务设置独立的上下文（90秒超时，避免任务卡住太久）
			taskCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()

			// 使用带超时的 channel 包装 FetchItems 调用
			// 这样即使 FetchItems 内部卡住，我们也能在超时后继续执行
			type fetchResult struct {
				resp *pb.FetchResponse
				err  error
			}
			resultCh := make(chan fetchResult, 1)

			go func() {
				resp, err := s.FetchItems(taskCtx, t)
				select {
				case resultCh <- fetchResult{resp: resp, err: err}:
				default:
					// 如果 channel 满了（主 goroutine 已超时离开），记录日志
					s.logger.Warn("fetch result discarded (timeout)",
						slog.String("task_id", taskID))
				}
			}()

			var resp *pb.FetchResponse
			var err error

			select {
			case result := <-resultCh:
				resp = result.resp
				err = result.err
			case <-taskCtx.Done():
				err = fmt.Errorf("task context timeout: %w", taskCtx.Err())
				s.logger.Error("task context timeout",
					slog.String("task_id", taskID),
					slog.Duration("elapsed", time.Since(taskStart)))
			}

			if err != nil {
				s.logger.Warn("crawl task failed",
					slog.String("task_id", taskID),
					slog.String("error", err.Error()),
					slog.Duration("duration", time.Since(taskStart)))
				// 即使失败也构造一个包含 TaskId 的响应以便追踪
				if resp == nil {
					resp = &pb.FetchResponse{ErrorMessage: err.Error(), TaskId: taskID}
				}
			}

			// 推送结果到 Redis
			if resp != nil {
				if resp.TaskId == "" {
					resp.TaskId = taskID
				}
				// 异步回传结果
				pushCtx, pushCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer pushCancel()

				if pushErr := s.redisQueue.PushResult(pushCtx, resp); pushErr != nil {
					s.logger.Error("push redis result failed", slog.String("error", pushErr.Error()))
				}
			}

			ackCtx, ackCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer ackCancel()
			if ackErr := s.redisQueue.AckTask(ackCtx, t); ackErr != nil {
				s.logger.Error("failed to ack task",
					slog.String("task_id", taskID),
					slog.String("error", ackErr.Error()))
			} else {
				s.logger.Debug("task acked", slog.String("task_id", taskID))
			}
		}(task)
	}
}

// startBrowser 根据配置启动浏览器。
//
// 它会根据配置决定是否使用 Headless 模式、代理以及是否下载默认浏览器。
// 针对 WSL2/容器环境做了适配（NoSandbox）。
//
// 参数:
//
//	cfg: 配置对象
//	logger: 日志记录器
//
// 返回值:
//
//	*rod.Browser: 连接好的浏览器实例
//	error: 启动失败返回错误
func startBrowser(ctx context.Context, cfg *config.Config, logger *slog.Logger, useProxy bool) (*rod.Browser, error) {
	bin := cfg.Browser.BinPath
	if bin == "" {
		logger.Info("no browser binary specified, downloading default...")
		path, err := launcher.NewBrowser().Get()
		if err != nil {
			return nil, fmt.Errorf("download browser: %w", err)
		}
		bin = path
	}

	// 针对 Docker/EC2 环境的 Flag 优化
	l := launcher.New().
		Headless(cfg.Browser.Headless).
		Bin(bin).
		NoSandbox(true).
		// 禁用 /dev/shm，防止容器内内存崩溃
		Set("disable-dev-shm-usage", "true").
		// 禁用 GPU，服务器环境不需要，节省资源
		Set("disable-gpu", "true").
		// 禁用软件光栅化器，进一步减少计算开销
		Set("disable-software-rasterizer", "true").
		Set("remote-allow-origins", "*").
		// 缓存与内存优化，减少磁盘写入压力
		Set("disk-cache-size", "1").
		Set("media-cache-size", "1").
		Set("disable-application-cache", "true").
		Set("js-flags", "--max_old_space_size=512")

	var proxyServer string
	var proxyUser string
	var proxyPass string

	// 读取并设置 HTTP 代理
	if useProxy {
		if cfg.Browser.ProxyURL == "" {
			return nil, fmt.Errorf("proxy enabled but no proxy url configured")
		}
		parsed, err := url.Parse(cfg.Browser.ProxyURL)
		if err != nil {
			return nil, fmt.Errorf("parse proxy url: %w", err)
		}
		if parsed.Scheme == "" || parsed.Host == "" {
			return nil, fmt.Errorf("invalid proxy url: %s", cfg.Browser.ProxyURL)
		}
		proxyServer = fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)
		if parsed.User != nil {
			proxyUser = parsed.User.Username()
			if pass, ok := parsed.User.Password(); ok {
				proxyPass = pass
			}
		}
		l = l.Proxy(proxyServer)
		if proxyUser != "" {
			logger.Info("using http proxy",
				slog.String("server", proxyServer),
				slog.String("auth_user", proxyUser))
		} else {
			logger.Info("using http proxy", slog.String("server", proxyServer))
		}
	}

	url, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("launch browser: %w", err)
	}

	browser := rod.New().Context(ctx).ControlURL(url)
	if err := browser.Connect(); err != nil {
		return nil, fmt.Errorf("connect browser: %w", err)
	}
	if proxyUser != "" {
		go browser.MustHandleAuth(proxyUser, proxyPass)()
		logger.Info("proxy authentication handler registered")
	}

	mode := "direct"
	if useProxy {
		mode = "proxy"
	}
	logger.Info("browser started", slog.String("bin", bin), slog.String("mode", mode))
	return browser, nil
}

func (s *Service) ensureBrowserState(ctx context.Context) error {
	shouldUseProxy, err := s.getProxyState(ctx)
	if err != nil {
		return err
	}
	s.mu.RLock()
	currentIsProxy := s.currentIsProxy
	s.mu.RUnlock()

	if shouldUseProxy == currentIsProxy {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	shouldUseProxy, err = s.getProxyState(ctx)
	if err != nil {
		return err
	}
	if shouldUseProxy == s.currentIsProxy {
		return nil
	}

	if err := s.rotateBrowser(shouldUseProxy); err != nil {
		return err
	}
	s.currentIsProxy = shouldUseProxy
	mode := "direct"
	if shouldUseProxy {
		mode = "proxy"
	}
	metrics.CrawlerProxyMode.Set(boolToGauge(shouldUseProxy))
	metrics.CrawlerProxySwitchTotal.WithLabelValues(mode).Inc()
	if shouldUseProxy {
		metrics.CrawlerProxySwitchToProxyTotal.Inc()
	}
	s.logger.Info("crawler mode switched", slog.String("mode", mode))
	return nil
}

func (s *Service) logPageTimeout(phase string, taskID string, url string, page *rod.Page, err error) {
	readyState := "unknown"
	if page != nil {
		if v, evalErr := page.Eval("document.readyState"); evalErr == nil {
			if state := v.Value.String(); state != "" {
				readyState = state
			}
		}
	}

	s.logger.Warn("page timeout",
		slog.String("phase", phase),
		slog.String("task_id", taskID),
		slog.String("url", url),
		slog.Duration("timeout", s.pageTimeout),
		slog.String("ready_state", readyState),
		slog.String("error", err.Error()))
}

// rotateBrowser 切换浏览器实例（需在持有 s.mu 写锁时调用）。
func (s *Service) rotateBrowser(useProxy bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	newBrowser, err := startBrowser(ctx, s.cfg, s.logger, useProxy)
	if err != nil {
		return err
	}
	oldBrowser := s.browser
	s.browser = newBrowser
	if oldBrowser != nil {
		if err := oldBrowser.Close(); err != nil {
			s.logger.Warn("close browser failed", slog.String("error", err.Error()))
		}
	}
	return nil
}

func (s *Service) isUsingProxy() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentIsProxy
}

func (s *Service) getProxyState(ctx context.Context) (bool, error) {
	now := time.Now()
	s.mu.RLock()
	if now.Before(s.proxyCacheUntil) {
		state := s.proxyCache
		s.mu.RUnlock()
		return state, nil
	}
	s.mu.RUnlock()

	if s.rdb == nil {
		return false, nil
	}

	// 为 Redis 调用设置短超时，避免卡住
	redisCtx, redisCancel := context.WithTimeout(ctx, 3*time.Second)
	defer redisCancel()

	exists, err := s.rdb.Exists(redisCtx, proxyCooldownKey).Result()
	if err != nil {
		// Redis 错误时使用缓存值（如果有）或默认值（降级策略）
		s.mu.RLock()
		cachedState := s.proxyCache
		s.mu.RUnlock()
		s.logger.Warn("get proxy state from redis failed, using cached value",
			slog.Bool("cached_state", cachedState),
			slog.String("error", err.Error()))
		return cachedState, nil // 降级：返回缓存值而不是错误
	}
	state := exists > 0

	s.mu.Lock()
	s.proxyCache = state
	s.proxyCacheUntil = now.Add(proxyCacheTTL)
	s.mu.Unlock()

	return state, nil
}

func (s *Service) setProxyCooldown(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		duration = s.cfg.App.ProxyCooldown
	}
	if s.rdb == nil {
		return errors.New("redis client is not initialized")
	}

	// 为 Redis 调用设置短超时
	redisCtx, redisCancel := context.WithTimeout(ctx, 3*time.Second)
	defer redisCancel()

	if err := s.rdb.Set(redisCtx, proxyCooldownKey, "1", duration).Err(); err != nil {
		s.logger.Warn("set proxy cooldown failed, updating local cache only",
			slog.String("error", err.Error()))
		// 即使 Redis 失败，也更新本地缓存（降级策略）
	}

	s.mu.Lock()
	s.proxyCache = true
	s.proxyCacheUntil = time.Now().Add(proxyCacheTTL)
	s.mu.Unlock()
	return nil
}

func boolToGauge(v bool) float64 {
	if v {
		return 1
	}
	return 0
}

// FetchItems 处理抓取请求。
//
// 流程：
// 1. 构建目标 URL
// 2. 打开新标签页（使用 Stealth 模式隐藏特征）
// 3. 导航至 URL 并等待页面加载
// 4. 解析商品列表元素
// 5. 返回抓取结果
//
// 注意：并发控制由 StartWorker 中的信号量管理，此方法直接执行抓取。
//
// 参数:
//
//	ctx: 上下文
//	req: gRPC 请求参数，包含关键词、价格区间等
//
// 返回值:
//
//	*pb.FetchResponse: 包含抓取到的商品列表
//	error: 执行过程中的错误
func (s *Service) FetchItems(ctx context.Context, req *pb.FetchRequest) (*pb.FetchResponse, error) {
	taskID := req.GetTaskId()
	start := time.Now()
	platform := strings.ToLower(req.GetPlatform().String())
	s.logger.Info("fetching items", slog.String("task_id", taskID), slog.String("platform", platform))

	// 统计计数
	s.stats.TotalProcessed.Add(1)
	metrics.ActiveTasks.Inc()
	defer metrics.ActiveTasks.Dec()

	recordMetrics := func(status string, err error) {
		metrics.CrawlerRequestsTotal.WithLabelValues(platform, status).Inc()
		metrics.CrawlerRequestDuration.WithLabelValues(platform).Observe(time.Since(start).Seconds())
		if err != nil {
			metrics.CrawlerErrorsTotal.WithLabelValues(platform, classifyCrawlerError(err)).Inc()
		}
	}

	recordModeMetrics := func(resp *pb.FetchResponse, err error) {
		mode := "direct"
		if s.isUsingProxy() {
			mode = "proxy"
		}
		status := "success"
		if err != nil {
			status = classifyCrawlStatus(err)
		} else if resp != nil && len(resp.Items) == 0 {
			status = "empty_result"
		}
		metrics.CrawlerRequestsByModeTotal.WithLabelValues(platform, status, mode).Inc()
		metrics.CrawlerRequestDurationByMode.WithLabelValues(platform, mode).Observe(time.Since(start).Seconds())
	}

	// 直接执行抓取（并发由 StartWorker 的 sem 控制）
	response, err := s.doCrawl(ctx, req, 0)

	// 更新任务计数
	if s.maxTasks > 0 {
		newCount := s.taskCounter.Add(1)
		metrics.CrawlerTasksProcessedCurrent.Set(float64(newCount))
		if newCount >= s.maxTasks {
			select {
			case s.restartCh <- struct{}{}:
				s.logger.Info("max tasks reached, signaling shutdown",
					slog.Uint64("count", newCount),
					slog.Uint64("limit", s.maxTasks))
			default:
			}
		}
	}

	if err != nil {
		s.stats.TotalFailed.Add(1)
		s.logger.Error("crawl failed",
			slog.String("task_id", taskID),
			slog.String("error", err.Error()),
			slog.Duration("duration", time.Since(start)),
		)
		recordMetrics("failed", err)
		recordModeMetrics(response, err)
		return nil, err
	}

	s.stats.TotalSucceeded.Add(1)
	if response != nil {
		response.TaskId = taskID
	}
	recordMetrics("success", nil)
	recordModeMetrics(response, nil)
	return response, nil
}

func (s *Service) doCrawl(ctx context.Context, req *pb.FetchRequest, attempt int) (*pb.FetchResponse, error) {
	taskID := req.GetTaskId()
	s.logger.Debug("doCrawl started", slog.String("task_id", taskID), slog.Int("attempt", attempt))

	// 为 ensureBrowserState 设置独立的短超时（防止 Redis 调用卡住）
	browserStateCtx, browserStateCancel := context.WithTimeout(ctx, 10*time.Second)
	defer browserStateCancel()
	if err := s.ensureBrowserState(browserStateCtx); err != nil {
		s.logger.Warn("ensureBrowserState failed", slog.String("task_id", taskID), slog.String("error", err.Error()))
		return nil, fmt.Errorf("ensure browser state: %w", err)
	}
	s.logger.Debug("browser state ensured", slog.String("task_id", taskID))

	if attempt == 0 && !s.isUsingProxy() && atomic.CompareAndSwapUint32(&s.forceProxyOnce, 1, 0) {
		s.logger.Warn("direct connection failed, activating proxy",
			slog.String("reason", "forced"),
			slog.Duration("cooldown", s.cfg.App.ProxyCooldown))
		if err := s.setProxyCooldown(ctx, s.cfg.App.ProxyCooldown); err != nil {
			return nil, err
		}
		return s.doCrawl(ctx, req, attempt+1)
	}

	response, err := s.crawlOnce(ctx, req)
	if err == nil {
		return response, nil
	}

	if attempt == 0 && !s.isUsingProxy() && shouldActivateProxy(err) {
		s.logger.Warn("direct connection failed, activating proxy",
			slog.Duration("cooldown", s.cfg.App.ProxyCooldown))
		if err := s.setProxyCooldown(ctx, s.cfg.App.ProxyCooldown); err != nil {
			return nil, err
		}
		return s.doCrawl(ctx, req, attempt+1)
	}

	return response, err
}

// crawlOnce 执行单次爬取逻辑（不包含自动切换与重试）。
func (s *Service) crawlOnce(ctx context.Context, req *pb.FetchRequest) (*pb.FetchResponse, error) {
	taskID := req.GetTaskId()
	crawlStart := time.Now()
	s.logger.Debug("crawlOnce started", slog.String("task_id", taskID))

	if s.rateLimiter != nil {
		s.logger.Debug("waiting for rate limit", slog.String("task_id", taskID))
		rateLimitStart := time.Now()
		// 为速率限制设置最大等待时间（30秒），防止无限等待
		rateLimitDeadline := time.After(30 * time.Second)
		for {
			// 为单次 Redis 调用设置短超时
			rateLimitCtx, rateLimitCancel := context.WithTimeout(ctx, 5*time.Second)
			allowed, err := s.rateLimiter.Allow(rateLimitCtx, rateLimitKey, int(s.cfg.App.RateLimit), int(s.cfg.App.RateBurst))
			rateLimitCancel()

			if err != nil {
				s.logger.Warn("rate limit check failed", slog.String("task_id", taskID), slog.String("error", err.Error()))
				// Redis 错误时，放行请求避免阻塞（降级策略）
				s.logger.Warn("rate limit degraded, allowing request", slog.String("task_id", taskID))
				break
			}
			if allowed {
				metrics.RateLimitWaitDuration.Observe(time.Since(rateLimitStart).Seconds())
				s.logger.Debug("rate limit acquired", slog.String("task_id", taskID), slog.Duration("wait_time", time.Since(rateLimitStart)))
				break
			}

			select {
			case <-ctx.Done():
				metrics.RateLimitWaitDuration.Observe(time.Since(rateLimitStart).Seconds())
				metrics.RateLimitTimeoutTotal.Inc()
				return nil, fmt.Errorf("rate limit wait timeout: %w", ctx.Err())
			case <-rateLimitDeadline:
				s.logger.Warn("rate limit max wait exceeded, allowing request", slog.String("task_id", taskID))
				metrics.RateLimitWaitDuration.Observe(time.Since(rateLimitStart).Seconds())
				goto RateLimitDone
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
RateLimitDone:

	url := BuildMercariURL(req)
	s.logger.Debug("built mercari URL", slog.String("task_id", taskID), slog.String("url", url))

	s.mu.RLock()
	browser := s.browser
	s.mu.RUnlock()
	if browser == nil {
		return nil, fmt.Errorf("browser not initialized")
	}

	// 重要：页面创建时使用任务的完整 context（不是短超时的 context）
	// 因为页面对象会继承这个 context，后续所有操作都受它限制
	// 我们只在外层用 select 做超时保护，不让 Page 对象绑定短超时 context
	s.logger.Debug("creating browser page", slog.String("task_id", taskID))

	type pageResult struct {
		page *rod.Page
		err  error
	}
	pageResultCh := make(chan pageResult, 1)

	// 页面创建使用任务 context（90秒），而不是短超时 context
	go func() {
		page, pageErr := browser.Context(ctx).Page(proto.TargetCreateTarget{URL: ""})
		select {
		case pageResultCh <- pageResult{page: page, err: pageErr}:
		default:
			// channel 满了，说明主 goroutine 已超时退出，需要清理页面
			if page != nil {
				_ = page.Close()
			}
			s.logger.Warn("page creation completed after timeout, cleaned up",
				slog.String("task_id", taskID))
		}
	}()

	// 页面创建的超时保护（10秒）- 只用于这个 select，不影响页面对象的内部 context
	pageCreateTimer := time.NewTimer(10 * time.Second)
	defer pageCreateTimer.Stop()

	var basePage *rod.Page
	var err error
	select {
	case result := <-pageResultCh:
		if result.err != nil {
			return nil, fmt.Errorf("create page failed: %w", result.err)
		}
		basePage = result.page
		s.logger.Debug("browser page created", slog.String("task_id", taskID))
	case <-pageCreateTimer.C:
		s.logger.Warn("page creation timeout", slog.String("task_id", taskID))
		return nil, fmt.Errorf("create page timeout after 10s")
	case <-ctx.Done():
		return nil, fmt.Errorf("context cancelled during page creation: %w", ctx.Err())
	}

	// Stealth 脚本应用 - 同样只用 select 做超时保护
	stealthTimer := time.NewTimer(5 * time.Second)
	defer stealthTimer.Stop()
	stealthDone := make(chan error, 1)
	go func() {
		_, evalErr := basePage.EvalOnNewDocument(stealth.JS)
		stealthDone <- evalErr
	}()

	select {
	case err = <-stealthDone:
		if err != nil {
			_ = basePage.Close()
			return nil, fmt.Errorf("apply stealth script: %w", err)
		}
	case <-stealthTimer.C:
		_ = basePage.Close()
		return nil, fmt.Errorf("apply stealth script timeout after 5s")
	case <-ctx.Done():
		_ = basePage.Close()
		return nil, fmt.Errorf("context cancelled during stealth script: %w", ctx.Err())
	}
	s.logger.Debug("stealth script applied", slog.String("task_id", taskID))

	page := basePage
	// 定义增强版屏蔽列表
	blockedURLs := []string{
		// 1. 高带宽资源 (图片/字体/媒体)
		"*.png", "*.jpg", "*.jpeg", "*.gif", "*.webp", "*.svg", "*.ico",
		"*.avif", "*.apng", "*.heic", "*.heif", "*.bmp", "*.tif", "*.tiff",
		"*.woff", "*.woff2", "*.ttf", "*.eot", "*.otf",
		"*.mp4", "*.webm", "*.m4v", "*.mov", "*.avi",
		"*.mp3", "*.aac", "*.m4a", "*.ogg", "*.wav", "*.flac",

		// 2. 广告与追踪脚本
		"*google-analytics*",
		"*googletagmanager*",
		"*doubleclick*",
		"*criteo*",
		"*facebook*",
		"*twitter*",
		"*appsflyer*",
		"*smartnews*",
		"*bing*",
		"*yahoo*",
		"*line-scdn*",
		"*popin*",

		"*tiktok*",           // analytics.tiktok.com
		"*sentry*",           // ingest.sentry.io (错误监控)
		"*syndicatedsearch*", // Google 广告组件
	}
	if err := (proto.NetworkSetBlockedURLs{
		Urls: blockedURLs,
	}).Call(page); err != nil {
		s.logger.Warn("set blocked urls failed", slog.String("error", err.Error()))
	}
	metrics.CrawlerBrowserActive.Inc()
	defer func() {
		metrics.CrawlerBrowserActive.Dec()
		_ = page.Close()
	}()

	// 设置超时、Stealth 与 UA。
	page = page.Timeout(s.pageTimeout)
	page.MustSetUserAgent(&proto.NetworkSetUserAgentOverride{UserAgent: s.defaultUA})

	s.logger.Info("loading page", slog.String("task_id", taskID), slog.String("url", url))

	// 使用带超时的 context 包装 Navigate 操作，确保即使浏览器卡住也能及时返回
	navigateCtx, navigateCancel := context.WithTimeout(ctx, s.pageTimeout)
	defer navigateCancel()

	// 在 goroutine 中执行 Navigate，如果超时则强制取消
	navigateErrCh := make(chan error, 1)
	go func() {
		navigateErrCh <- page.Navigate(url)
	}()

	select {
	case navErr := <-navigateErrCh:
		if navErr != nil {
			return nil, fmt.Errorf("navigate: %w", navErr)
		}
	case <-navigateCtx.Done():
		return nil, fmt.Errorf("navigate timeout: %w", navigateCtx.Err())
	}

	info, err := page.Info()
	if err == nil {
		s.logger.Info("page loaded",
			slog.String("title", info.Title),
			slog.String("actual_url", info.URL),
		)
	}

	if s.isBlockedPage(page) {
		return nil, fmt.Errorf("blocked_page")
	}

	// 等待 [data-testid="item-cell"] 内部出现 <a> 标签，避免骨架屏阶段
	// 使用带超时的 context 包装 Race 操作，确保即使浏览器卡住也能及时返回
	raceCtx, raceCancel := context.WithTimeout(ctx, s.pageTimeout)
	defer raceCancel()

	raceErrCh := make(chan error, 1)
	go func() {
		_, raceErr := page.Race().
			Element(`[data-testid="item-cell"] a`).Handle(func(e *rod.Element) error {
			return nil
		}).
			Element(`.merEmptyState`).Handle(func(e *rod.Element) error {
			return fmt.Errorf("no_items_state")
		}).
			Do()
		raceErrCh <- raceErr
	}()

	select {
	case err = <-raceErrCh:
		// 正常返回，继续处理
	case <-raceCtx.Done():
		err = fmt.Errorf("race timeout: %w", raceCtx.Err())
		s.logPageTimeout("wait_for_items", taskID, url, page, raceCtx.Err())
	}

	if err != nil {
		// 如果检测到空状态，或者 Race 超时/失败后通过文本再次确认（作为兜底）
		if err.Error() == "no_items_state" || s.isNoItemsPage(page) {
			s.logger.Info("no items found",
				slog.String("task_id", taskID),
				slog.String("url", url),
				slog.String("duration", time.Since(crawlStart).String()),
			)
			return &pb.FetchResponse{
				Items:        []*pb.Item{},
				TotalFound:   0,
				ErrorMessage: "no_items",
			}, nil
		}
		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "context deadline exceeded") {
			s.logPageTimeout("wait_for_items", taskID, url, page, err)
		}
		return nil, fmt.Errorf("wait for items: %w", err)
	}

	// 滚动加载更多商品，直到达到 maxFetchCount 或无法加载更多
	selector := `[data-testid="item-cell"]:has(a)`
	limit := s.maxFetchCount
	if limit <= 0 {
		limit = 50 // 默认保底
	}

	timeout := time.After(s.pageTimeout) // 总超时控制
	noGrowthAttempts := 0

	countItems := func() (int, error) {
		// 使用带超时的 context 包装 Elements 操作，避免在滚动循环中卡住
		countCtx, countCancel := context.WithTimeout(ctx, 5*time.Second)
		defer countCancel()

		type countResult struct {
			count int
			err   error
		}
		countResultCh := make(chan countResult, 1)
		go func() {
			elems, elemErr := page.Elements(selector)
			if elemErr != nil {
				countResultCh <- countResult{count: 0, err: elemErr}
				return
			}
			countResultCh <- countResult{count: len(elems), err: nil}
		}()

		select {
		case result := <-countResultCh:
			return result.count, result.err
		case <-countCtx.Done():
			return 0, fmt.Errorf("count items timeout: %w", countCtx.Err())
		}
	}

	// 滚动循环
	for {
		currentCount, err := countItems()
		if err != nil {
			break
		}
		if currentCount >= limit {
			break
		}

		// 滚动策略：逐步向下滚动，而不是直接到底部，确保 Lazy Load 触发
		// 获取当前滚动高度
		_, _ = page.Eval(`window.scrollBy(0, window.innerHeight)`)

		// 等待加载... 使用 Race 来处理超时
		select {
		case <-timeout:
			goto ParseItems // 超时跳出循环
		default:
			time.Sleep(500 * time.Millisecond) // 等待页面渲染
		}

		afterCount, err := countItems()
		if err != nil {
			break
		}
		if afterCount <= currentCount {
			noGrowthAttempts++
			if noGrowthAttempts >= 3 && currentCount > 0 {
				break
			}
		} else {
			noGrowthAttempts = 0
		}
	}

ParseItems:
	// 解析商品列表。
	// 使用带超时的 context 包装 Elements 操作，确保即使浏览器卡住也能及时返回
	elementsCtx, elementsCancel := context.WithTimeout(ctx, s.pageTimeout)
	defer elementsCancel()

	type elementsResult struct {
		elements rod.Elements
		err      error
	}
	elementsResultCh := make(chan elementsResult, 1)
	go func() {
		elems, elemErr := page.Elements(selector)
		elementsResultCh <- elementsResult{elements: elems, err: elemErr}
	}()

	var elements rod.Elements
	select {
	case result := <-elementsResultCh:
		elements = result.elements
		err = result.err
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "context deadline exceeded") || strings.Contains(err.Error(), "timeout") {
				s.logPageTimeout("get_elements", taskID, url, page, err)
			}
			return nil, fmt.Errorf("get elements: %w", err)
		}
	case <-elementsCtx.Done():
		err = fmt.Errorf("get elements timeout: %w", elementsCtx.Err())
		s.logPageTimeout("get_elements", taskID, url, page, elementsCtx.Err())
		return nil, err
	}
	if len(elements) == 0 {
		return &pb.FetchResponse{
			Items:        []*pb.Item{},
			TotalFound:   0,
			ErrorMessage: "",
		}, nil
	}

	// 提取商品信息。
	items := make([]*pb.Item, 0, limit)
	skipCount := 0
	for i, el := range elements {
		if len(items) >= limit {
			break
		}
		item, err := extractItem(el)
		if err != nil {
			skipCount++
			if skipCount <= 3 {
				s.logger.Warn("extract item failed",
					slog.String("task_id", taskID),
					slog.Int("index", i),
					slog.String("error", err.Error()))
			}
			continue
		}
		item.Platform = req.GetPlatform()
		item.Currency = "JPY"
		items = append(items, item)
	}

	s.logger.Info("found items",
		slog.String("task_id", taskID),
		slog.Int("count", len(items)),
		slog.Int("skipped", skipCount))
	s.logger.Info("crawl completed",
		slog.String("task_id", taskID),
		slog.Int("count", len(items)),
		slog.String("duration", time.Since(crawlStart).String()))
	return &pb.FetchResponse{
		Items:        items,
		TotalFound:   int32(len(items)),
		ErrorMessage: "",
	}, nil
}

func (s *Service) isNoItemsPage(page *rod.Page) bool {
	// 使用 Elements 而不是 Element，避免在元素不存在时阻塞等待超时
	if elems, err := page.Elements(".merEmptyState"); err == nil && len(elems) > 0 {
		return true
	}

	// 使用带超时的 Page 克隆进行文本检查，避免卡顿
	pWithTimeout := page.Timeout(2 * time.Second)
	body, err := pWithTimeout.Element("body")
	if err != nil {
		return false
	}
	text, err := body.Text()
	if err != nil {
		return false
	}
	return isNoItemsText(text)
}

func (s *Service) isBlockedPage(page *rod.Page) bool {
	pWithTimeout := page.Timeout(2 * time.Second)
	body, err := pWithTimeout.Element("body")
	if err != nil {
		return false
	}
	text, err := body.Text()
	if err != nil {
		return false
	}
	return isBlockedText(text)
}

func isNoItemsText(text string) bool {
	if text == "" {
		return false
	}
	noItemsHints := []string{
		"出品された商品がありません",
		"該当する商品はありません",
		"検索結果はありません",
		"商品が見つかりません",
		"見つかりませんでした",
		"検索結果がありません",
	}
	for _, hint := range noItemsHints {
		if strings.Contains(text, hint) {
			return true
		}
	}
	return false
}

func isBlockedText(text string) bool {
	if text == "" {
		return false
	}
	lower := strings.ToLower(text)
	blockHints := []string{
		"cloudflare",
		"attention required",
		"verify you are human",
		"access denied",
		"temporarily unavailable",
	}
	for _, hint := range blockHints {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}

func shouldActivateProxy(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "blocked_page") ||
		strings.Contains(msg, "cloudflare") ||
		strings.Contains(msg, "attention required") ||
		strings.Contains(msg, "access denied") ||
		strings.Contains(msg, "403") ||
		strings.Contains(msg, "429") ||
		strings.Contains(msg, "forbidden") ||
		strings.Contains(msg, "too many requests") {
		return true
	}
	if strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded") {
		return true
	}
	if strings.Contains(msg, "net::") ||
		strings.Contains(msg, "connection") ||
		strings.Contains(msg, "navigate") {
		return true
	}
	return false
}

func classifyCrawlerError(err error) string {
	if err == nil {
		return "unknown"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "timeout"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "timeout"):
		return "timeout"
	case strings.Contains(msg, "navigate") || strings.Contains(msg, "net::") || strings.Contains(msg, "connection"):
		return "network_error"
	case strings.Contains(msg, "parse") || strings.Contains(msg, "extract"):
		return "parse_error"
	default:
		return "unknown"
	}
}

func classifyCrawlStatus(err error) string {
	if err == nil {
		return "success"
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "403") ||
		strings.Contains(msg, "forbidden") ||
		strings.Contains(msg, "access denied") ||
		strings.Contains(msg, "blocked_page") {
		return "403_forbidden"
	}
	return "error"
}

// extractItem 从单个 DOM 元素中提取商品信息。
// 这里的关键是：不依赖 <img> 标签的存在，因为资源屏蔽可能导致它被 DOM 移除。
func extractItem(el *rod.Element) (*pb.Item, error) {
	// 1. 提取链接 (<a>) - 这是核心，必须存在
	link, err := el.Element("a")
	if err != nil {
		return nil, fmt.Errorf("link: %w", err)
	}
	href, _ := link.Attribute("href")
	itemURL := ""
	if href != nil {
		itemURL = *href
	}

	// 2. 提取 ID (从链接中)
	id := ""
	if itemURL != "" {
		// 逻辑：匹配 /item/m... 或 /shops/product/...
		if strings.Contains(itemURL, "/item/") {
			parts := strings.Split(itemURL, "/")
			if len(parts) > 0 {
				possibleID := parts[len(parts)-1]
				if strings.HasPrefix(possibleID, "m") {
					id = possibleID
				}
			}
		} else if strings.Contains(itemURL, "/shops/product/") {
			parts := strings.Split(itemURL, "/")
			if len(parts) > 0 {
				id = "shops_" + parts[len(parts)-1]
			}
		}
	}

	// 3. 提取标题
	// 策略：优先找 data-testid="thumbnail-item-name" (最稳)，找不到再尝试找 img alt (兼容旧版)
	titleStr := ""
	if titleEl, err := el.Element(`[data-testid="thumbnail-item-name"]`); err == nil {
		titleStr, _ = titleEl.Text()
	} else {
		// 只有在找不到文本节点时，才尝试去找 img (此时 img 不存在也没关系，只是标题为空)
		if img, err := el.Element("img"); err == nil {
			if alt, _ := img.Attribute("alt"); alt != nil {
				titleStr = strings.TrimSuffix(*alt, "のサムネイル")
			}
		}
	}

	// 4. 提取价格
	priceVal, err := extractPriceHelper(el)
	if err != nil {
		return nil, fmt.Errorf("price not found or zero: %w", err)
	}

	// 5. 构造图片 URL (完全不依赖 img src)
	imageURL := ""
	if id != "" && strings.HasPrefix(id, "m") {
		// 标准 Mercari 图片规则
		imageURL = fmt.Sprintf("https://static.mercdn.net/thumb/item/webp/%s_1.jpg", id)
	}
	if imageURL == "" {
		if img, err := el.Element("img"); err == nil {
			if src, _ := img.Attribute("src"); src != nil {
				imageURL = *src
			}
		}
	}

	// 6. 状态判断
	status := "on_sale"
	if txt, err := el.Text(); err == nil {
		lower := strings.ToLower(txt)
		if strings.Contains(lower, "sold") || strings.Contains(txt, "売り切れ") {
			status = "sold"
		}
	}

	itemURL = normalizeMercariURL(itemURL)

	return &pb.Item{
		SourceId: id,
		Title:    titleStr,
		Price:    int32(priceVal),
		ImageUrl: imageURL,
		ItemUrl:  itemURL,
		Status:   status,
	}, nil
}

// parsePrice 将价格字符串转换为整数。
//
// 它会移除货币符号（¥）和千位分隔符（,），然后解析数字。
//
// 参数:
//
//	txt: 原始价格字符串，如 "¥ 1,200"
//
// 返回值:
//
//	int64: 解析后的数值
//	error: 解析失败返回错误
func parsePrice(txt string) (int64, error) {
	if match := priceWithCurrencyRe.FindStringSubmatch(txt); len(match) > 1 {
		candidate := strings.ReplaceAll(match[1], ",", "")
		val, err := strconv.ParseInt(candidate, 10, 64)
		if err == nil {
			return val, nil
		}
	}

	cleaned := strings.ReplaceAll(txt, "¥", "")
	cleaned = strings.ReplaceAll(cleaned, "￥", "")
	cleaned = strings.ReplaceAll(cleaned, ",", "")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return 0, fmt.Errorf("empty price")
	}
	matches := priceRe.FindAllString(cleaned, -1)
	if len(matches) == 0 {
		return 0, fmt.Errorf("no digits")
	}
	var bestVal int64
	bestLen := 0
	found := false
	for _, match := range matches {
		val, err := strconv.ParseInt(match, 10, 64)
		if err != nil {
			continue
		}
		if !found || len(match) > bestLen || (len(match) == bestLen && val > bestVal) {
			bestVal = val
			bestLen = len(match)
			found = true
		}
	}
	if !found {
		return 0, fmt.Errorf("no valid digits")
	}
	return bestVal, nil
}

// extractPriceHelper 用于从价格元素中提取纯数字
// 输入: "1,999", "¥1,999", "¥ 200"
// 输出: 1999, 200
func extractPriceHelper(el *rod.Element) (int64, error) {
	containerSelectors := []string{
		".merPrice",
		"span[class^='merPrice']",
		"[data-testid='price']",
	}
	for _, sel := range containerSelectors {
		container, err := el.Element(sel)
		if err != nil {
			continue
		}
		if numEl, err := container.Element("span[class^='number']"); err == nil {
			if txt, err := numEl.Text(); err == nil && txt != "" {
				if price, err := parsePrice(txt); err == nil {
					return price, nil
				}
			}
		}
		if txt, err := container.Text(); err == nil && txt != "" {
			if price, err := parsePrice(txt); err == nil {
				return price, nil
			}
		}
	}
	return 0, fmt.Errorf("price element not found")
}

// normalizeMercariURL 将相对或协议省略的链接补全为完整的 Mercari URL。
func normalizeMercariURL(u string) string {
	if u == "" {
		return u
	}
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u
	}
	if strings.HasPrefix(u, "//") {
		return "https:" + u
	}
	if strings.HasPrefix(u, "/") {
		return "https://jp.mercari.com" + u
	}
	return "https://jp.mercari.com" + u
}

// Shutdown 优雅关闭爬虫服务。
//
// 关闭顺序：
// 1. 停止后台任务（健康检查、卡住任务清理）
// 2. 关闭浏览器实例
// 3. 关闭 Redis 连接
//
// 参数:
//
//	ctx: 上下文，用于控制关闭超时
//
// 返回值:
//
//	error: 关闭过程中的错误
func (s *Service) Shutdown(ctx context.Context) error {
	s.logger.Info("shutting down crawler service...")

	// 1. 停止后台任务
	if s.bgCancel != nil {
		s.bgCancel()
	}

	// 2. 关闭浏览器
	s.mu.Lock()
	browser := s.browser
	s.browser = nil
	s.mu.Unlock()
	if browser != nil {
		if err := browser.Close(); err != nil {
			s.logger.Error("close browser failed", slog.String("error", err.Error()))
		} else {
			metrics.CrawlerBrowserInstances.Dec()
		}
	}

	// 3. 关闭 Redis
	if s.rdb != nil {
		if err := s.rdb.Close(); err != nil {
			s.logger.Warn("close redis failed", slog.String("error", err.Error()))
		}
	}

	s.logger.Info("crawler service shutdown completed",
		slog.Int64("total_processed", s.stats.TotalProcessed.Load()),
		slog.Int64("total_succeeded", s.stats.TotalSucceeded.Load()),
		slog.Int64("total_failed", s.stats.TotalFailed.Load()),
	)
	return nil
}

// CrawlerStats 爬虫统计信息快照
type CrawlerStats struct {
	TotalProcessed int64
	TotalSucceeded int64
	TotalFailed    int64
	TotalPanics    int64
}

// Stats 获取爬虫服务的统计信息。
func (s *Service) Stats() CrawlerStats {
	return CrawlerStats{
		TotalProcessed: s.stats.TotalProcessed.Load(),
		TotalSucceeded: s.stats.TotalSucceeded.Load(),
		TotalFailed:    s.stats.TotalFailed.Load(),
		TotalPanics:    s.stats.TotalPanics.Load(),
	}
}
