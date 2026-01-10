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
	"goodshunter/internal/pkg/queue"
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
// 它维护了一个 rod.Browser 实例，并使用 worker pool 控制并发抓取数量。
type Service struct {
	browser         *rod.Browser
	queue           *queue.Queue // Worker Pool 用于控制并发
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
	taskCounter     atomic.Uint64
	maxTasks        uint64
	restartCh       chan struct{}
	redisQueue      *redisqueue.Client
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
	browser, err := startBrowser(cfg, logger, false)
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

	// 创建 Worker Pool
	// workers 数量使用浏览器的最大并发数
	// 队列容量设置为并发数的 2 倍，避免请求积压
	workers := cfg.Browser.MaxConcurrency
	capacity := workers * 2
	q := queue.NewQueue(logger, workers, capacity)

	// 设置错误处理器：记录爬取任务失败
	q.SetErrorHandler(func(err error, job queue.Job) {
		logger.Error("crawler job execution failed",
			slog.String("error", err.Error()))
	})

	// 启动 worker pool
	q.Start(ctx)
	logger.Info("crawler worker pool started",
		slog.Int("workers", workers),
		slog.Int("capacity", capacity))

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

	service := &Service{
		browser:        browser,
		queue:          q,
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
	}
	metrics.CrawlerProxyMode.Set(0)
	return service, nil
}

// RestartSignal exposes the restart notification channel.
func (s *Service) RestartSignal() <-chan struct{} {
	return s.restartCh
}

// StartWorker runs a Redis task consumption loop until ctx is canceled.
func (s *Service) StartWorker(ctx context.Context) error {
	if s.redisQueue == nil {
		return errors.New("redis queue client is not initialized")
	}

	concurrencyLimit := s.cfg.Browser.MaxConcurrency + 2 // 限制拉取速率，避免内存队列阻塞
	if concurrencyLimit < 1 {
		concurrencyLimit = 1
	}
	sem := make(chan struct{}, concurrencyLimit)
	s.logger.Info("crawler worker started",
		slog.Int("concurrency_limit", concurrencyLimit),
		slog.Int("browser_max_concurrency", s.cfg.Browser.MaxConcurrency))

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

		// 3. 处理任务（在独立 goroutine 中）
		go func(t *pb.FetchRequest) {
			defer func() {
				<-sem // 任务处理完成，释放令牌
			}()

			// 为每个任务设置独立的上下文
			taskCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()

			// FetchItems 内部会通过 worker pool 进一步控制浏览器并发
			resp, err := s.FetchItems(taskCtx, t)
			if err != nil {
				s.logger.Warn("crawl task failed",
					slog.String("task_id", t.GetTaskId()),
					slog.String("error", err.Error()))
				// 即使失败也构造一个包含 TaskId 的响应以便追踪
				if resp == nil {
					resp = &pb.FetchResponse{ErrorMessage: err.Error(), TaskId: t.GetTaskId()}
				}
			}
			// 推送结果到 Redis
			if resp != nil {
				if resp.TaskId == "" {
					resp.TaskId = t.GetTaskId()
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
					slog.String("task_id", t.GetTaskId()),
					slog.String("error", ackErr.Error()))
			} else {
				s.logger.Debug("task acked", slog.String("task_id", t.GetTaskId()))
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
func startBrowser(cfg *config.Config, logger *slog.Logger, useProxy bool) (*rod.Browser, error) {
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

	browser := rod.New().ControlURL(url)
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
	newBrowser, err := startBrowser(s.cfg, s.logger, useProxy)
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
	exists, err := s.rdb.Exists(ctx, proxyCooldownKey).Result()
	if err != nil {
		return false, fmt.Errorf("get proxy cooldown: %w", err)
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
	if err := s.rdb.Set(ctx, proxyCooldownKey, "1", duration).Err(); err != nil {
		return fmt.Errorf("set proxy cooldown: %w", err)
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
// 1. 将抓取任务提交到 worker pool
// 2. 构建目标 URL
// 3. 打开新标签页（使用 Stealth 模式隐藏特征）
// 4. 导航至 URL 并等待页面加载
// 5. 解析商品列表元素
// 6. 返回抓取结果
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

	// 创建一个 channel 用于接收抓取结果
	resultCh := make(chan *fetchResult, 1)

	// 创建 Job 函数
	job := func(workerCtx context.Context) error {
		response, err := s.doCrawl(ctx, req, 0)

		// 将结果发送到 channel
		select {
		case resultCh <- &fetchResult{response: response, err: err}:
		case <-workerCtx.Done():
			return workerCtx.Err()
		}

		return err
	}

	// 提交到 worker pool（阻塞式，直到队列有空间）
	if err := s.queue.EnqueueBlocking(ctx, job); err != nil {
		recordMetrics("failed", err)
		recordModeMetrics(nil, err)
		return nil, fmt.Errorf("enqueue crawl job: %w", err)
	}

	// 等待结果
	select {
	case result := <-resultCh:
		if result.response != nil {
			result.response.TaskId = taskID
		}
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

		if result.err != nil {
			s.logger.Error("crawl failed",
				slog.String("task_id", taskID),
				slog.String("error", result.err.Error()),
				slog.String("duration", time.Since(start).String()),
			)
			recordMetrics("failed", result.err)
			recordModeMetrics(result.response, result.err)
			return nil, result.err
		}
		recordMetrics("success", nil)
		recordModeMetrics(result.response, nil)
		return result.response, nil
	case <-ctx.Done():
		recordMetrics("failed", ctx.Err())
		recordModeMetrics(nil, ctx.Err())
		return nil, ctx.Err()
	}
}

// fetchResult 保存抓取任务的执行结果
type fetchResult struct {
	response *pb.FetchResponse
	err      error
}

func (s *Service) doCrawl(ctx context.Context, req *pb.FetchRequest, attempt int) (*pb.FetchResponse, error) {
	if err := s.ensureBrowserState(ctx); err != nil {
		return nil, err
	}

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
	start := time.Now()

	if s.rateLimiter != nil {
		start := time.Now()
		for {
			allowed, err := s.rateLimiter.Allow(ctx, rateLimitKey, int(s.cfg.App.RateLimit), int(s.cfg.App.RateBurst))
			if err != nil {
				return nil, fmt.Errorf("rate limit: %w", err)
			}
			if allowed {
				metrics.RateLimitWaitDuration.Observe(time.Since(start).Seconds())
				break
			}

			select {
			case <-ctx.Done():
				metrics.RateLimitWaitDuration.Observe(time.Since(start).Seconds())
				metrics.RateLimitTimeoutTotal.Inc()
				return nil, fmt.Errorf("rate limit wait timeout: %w", ctx.Err())
			case <-time.After(50 * time.Millisecond):
			}
		}
	}

	url := BuildMercariURL(req)
	s.mu.RLock()
	browser := s.browser
	s.mu.RUnlock()
	if browser == nil {
		return nil, fmt.Errorf("browser not initialized")
	}
	page := stealth.MustPage(browser)
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
	if err := page.Navigate(url); err != nil {
		return nil, fmt.Errorf("navigate: %w", err)
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
	_, err = page.Race().
		Element(`[data-testid="item-cell"] a`).Handle(func(e *rod.Element) error {
		return nil
	}).
		Element(`.merEmptyState`).Handle(func(e *rod.Element) error {
		return fmt.Errorf("no_items_state")
	}).
		Do()

	if err != nil {
		// 如果检测到空状态，或者 Race 超时/失败后通过文本再次确认（作为兜底）
		if err.Error() == "no_items_state" || s.isNoItemsPage(page) {
			s.logger.Info("no items found",
				slog.String("task_id", taskID),
				slog.String("url", url),
				slog.String("duration", time.Since(start).String()),
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
		elements, err := page.Elements(selector)
		if err != nil {
			return 0, err
		}
		return len(elements), nil
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
	elements, err := page.Elements(selector)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "context deadline exceeded") {
			s.logPageTimeout("get_elements", taskID, url, page, err)
		}
		return nil, fmt.Errorf("get elements: %w", err)
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
		slog.String("duration", time.Since(start).String()))
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
// 它会等待所有正在执行的爬取任务完成。
//
// 参数:
//
//	ctx: 上下文，用于控制关闭超时
//
// 返回值:
//
//	error: 关闭过程中的错误
func (s *Service) Shutdown(ctx context.Context) error {
	s.logger.Info("shutting down crawler worker pool...")

	// 从 context 获取超时时间
	deadline, ok := ctx.Deadline()
	var timeout time.Duration
	if ok {
		timeout = time.Until(deadline)
	} else {
		timeout = 30 * time.Second // 默认 30 秒
	}

	// 等待所有爬取任务完成
	if err := s.queue.ShutdownWithTimeout(timeout); err != nil {
		return fmt.Errorf("shutdown worker pool: %w", err)
	}

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

	if s.rdb != nil {
		if err := s.rdb.Close(); err != nil {
			s.logger.Warn("close redis failed", slog.String("error", err.Error()))
		}
	}

	s.logger.Info("crawler worker pool shutdown completed")
	return nil
}

// Stats 获取爬虫服务的统计信息。
//
// 返回值:
//
//	queue.QueueStats: 队列统计信息快照
func (s *Service) Stats() queue.QueueStats {
	return s.queue.Stats()
}
