package crawler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"goodshunter/internal/config"
	"goodshunter/internal/pkg/metrics"
	"goodshunter/internal/pkg/queue"
	"goodshunter/internal/pkg/ratelimit"
	"goodshunter/proto/pb"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
	"github.com/redis/go-redis/v9"
)

var (
	priceRe = regexp.MustCompile(`[0-9]+`)
	idRe    = regexp.MustCompile(`m[0-9]+`)
)

// Service 实现爬虫 gRPC 服务，负责浏览器调度与页面解析。
//
// 它维护了一个 rod.Browser 实例，并使用 worker pool 控制并发抓取数量。
type Service struct {
	pb.UnimplementedCrawlerServiceServer
	browser       *rod.Browser
	queue         *queue.Queue // Worker Pool 用于控制并发
	rdb           *redis.Client
	rateLimiter   *ratelimit.RateLimiter
	logger        *slog.Logger
	defaultUA     string
	pageTimeout   time.Duration
	maxFetchCount int
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
func NewService(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*Service, error) {
	browser, err := startBrowser(cfg, logger)
	if err != nil {
		return nil, err
	}
	metrics.CrawlerBrowserInstances.Inc()

	var rdb *redis.Client
	var limiter *ratelimit.RateLimiter
	if cfg.App.RateLimit > 0 && cfg.App.RateBurst > 0 {
		rdb = redis.NewClient(&redis.Options{
			Addr:     cfg.Redis.Addr,
			Password: cfg.Redis.Password,
		})
		if err := rdb.Ping(ctx).Err(); err != nil {
			return nil, fmt.Errorf("connect redis for rate limit: %w", err)
		}
		limiter = ratelimit.NewRedisRateLimiter(rdb, logger, "goodshunter:ratelimit:crawler", cfg.App.RateLimit, cfg.App.RateBurst)
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

	return &Service{
		browser:       browser,
		queue:         q,
		rdb:           rdb,
		rateLimiter:   limiter,
		logger:        logger,
		defaultUA:     "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/118.0.0.0 Safari/537.36",
		pageTimeout:   30 * time.Second,
		maxFetchCount: cfg.Browser.MaxFetchCount,
	}, nil
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
func startBrowser(cfg *config.Config, logger *slog.Logger) (*rod.Browser, error) {
	bin := cfg.Browser.BinPath
	if bin == "" {
		logger.Info("no browser binary specified, downloading default...")
		path, err := launcher.NewBrowser().Get()
		if err != nil {
			return nil, fmt.Errorf("download browser: %w", err)
		}
		bin = path
	}

	l := launcher.New().
		Headless(cfg.Browser.Headless).
		Bin(bin).
		NoSandbox(true).
		Set("remote-allow-origins", "*") // 解决某些环境下的 CORS 问题

	if cfg.Browser.ProxyURL != "" {
		l = l.Proxy(cfg.Browser.ProxyURL)
	}

	url, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("launch browser: %w", err)
	}

	browser := rod.New().ControlURL(url)
	if err := browser.Connect(); err != nil {
		return nil, fmt.Errorf("connect browser: %w", err)
	}

	logger.Info("browser started", slog.String("bin", bin))
	return browser, nil
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

	// 创建一个 channel 用于接收抓取结果
	resultCh := make(chan *fetchResult, 1)

	// 创建 Job 函数
	job := func(workerCtx context.Context) error {
		response, err := s.executeCrawl(ctx, req)

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
		return nil, fmt.Errorf("enqueue crawl job: %w", err)
	}

	// 等待结果
	select {
	case result := <-resultCh:
		if result.err != nil {
			s.logger.Error("crawl failed",
				slog.String("task_id", taskID),
				slog.String("error", result.err.Error()),
				slog.String("duration", time.Since(start).String()),
			)
			recordMetrics("failed", result.err)
			return nil, result.err
		}
		recordMetrics("success", nil)
		return result.response, nil
	case <-ctx.Done():
		recordMetrics("failed", ctx.Err())
		return nil, ctx.Err()
	}
}

// fetchResult 保存抓取任务的执行结果
type fetchResult struct {
	response *pb.FetchResponse
	err      error
}

// executeCrawl 执行实际的爬取逻辑
func (s *Service) executeCrawl(ctx context.Context, req *pb.FetchRequest) (*pb.FetchResponse, error) {
	taskID := req.GetTaskId()
	start := time.Now()

	if s.rateLimiter != nil {
		if err := s.rateLimiter.Acquire(ctx); err != nil {
			if errors.Is(err, ratelimit.ErrRateLimitTimeout) {
				return nil, fmt.Errorf("rate limit wait timeout: %w", err)
			}
			return nil, fmt.Errorf("rate limit: %w", err)
		}
	}

	// 构建 URL 并打开新页面。
	url := BuildMercariURL(req)
	page := stealth.MustPage(s.browser)
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

	// 等待列表元素加载完成。
	// 使用 Race 同时等待商品列表或空状态标志，避免在无商品时死等直到超时
	_, err = page.Race().
		Element(`[data-testid="item-cell"]`).Handle(func(e *rod.Element) error {
		return nil // 找到商品
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
		return nil, fmt.Errorf("wait for items: %w", err)
	}

	// 滚动加载更多商品，直到达到 maxFetchCount 或无法加载更多
	selector := `[data-testid="item-cell"]`
	limit := s.maxFetchCount
	if limit <= 0 {
		limit = 50 // 默认保底
	}

	lastCount := 0
	timeout := time.After(s.pageTimeout) // 总超时控制

	// 滚动循环
	for {
		// 检查当前数量 (仅统计已加载图片的有效商品)
		// 这一步至关重要：因为 DOM 可能预先存在空壳元素，如果只查 item-cell 会导致提前结束滚动，
		// 从而导致后续提取时因图片未加载而失败。
		loadedElements, err := page.Elements(selector + " img")
		if err != nil {
			break
		}
		currentCount := len(loadedElements)
		if currentCount >= limit {
			break
		}

		// 如果数量不再增加（且已经尝试过滚动），则停止
		if currentCount == lastCount && currentCount > 0 {
			// 可以增加一些重试次数或更智能的检测，这里简单处理：
			// 如果没变，尝试再滚一次，如果还是没变就退出？
			// 为了避免死循环，这里假设 rod 的 Wait 会处理好，
			// 我们简单地：如果没变，等待短时间后再次检查，如果还是没变则退出。
			time.Sleep(500 * time.Millisecond)
			elements2, _ := page.Elements(selector)
			if len(elements2) == lastCount {
				break
			}
			currentCount = len(elements2)
		}
		lastCount = currentCount

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
	}

ParseItems:
	// 解析商品列表。
	elements, err := page.Elements(selector)
	if err != nil {
		return nil, fmt.Errorf("get elements: %w", err)
	}
	if len(elements) == 0 {
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
		slog.String("url", url),
		slog.Int("elements", len(elements)),
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

// extractItem 从单个 DOM 元素中提取商品信息。
//
// 参数:
//
//	el: 代表单个商品的 DOM 元素
//
// 返回值:
//
//	*pb.Item: 提取出的商品结构体
//	error: 解析失败返回错误
func extractItem(el *rod.Element) (*pb.Item, error) {
	// 解析主图与标题。
	img, err := el.Element("img")
	if err != nil {
		return nil, fmt.Errorf("img: %w", err)
	}
	title, err := img.Attribute("alt")
	if err != nil {
		return nil, fmt.Errorf("img alt: %w", err)
	}
	src, err := img.Attribute("src")
	if err != nil {
		return nil, fmt.Errorf("img src: %w", err)
	}

	titleStr := ""
	if title != nil {
		titleStr = strings.TrimSuffix(*title, "のサムネイル")
	}

	// 解析商品链接。
	link, err := el.Element("a")
	if err != nil {
		return nil, fmt.Errorf("link: %w", err)
	}
	href, _ := link.Attribute("href")
	itemURL := ""
	if href != nil {
		itemURL = *href
	}

	// 从图片 URL 提取商品 ID（m\d+）。
	id := ""
	if src != nil {
		if m := idRe.FindString(*src); m != "" {
			id = m
		}
	}

	// 如果从图片无法提取 ID，尝试从链接提取
	if id == "" && itemURL != "" {
		// 普通商品: /item/m123456
		if strings.Contains(itemURL, "/item/") {
			parts := strings.Split(itemURL, "/")
			if len(parts) > 0 {
				possibleID := parts[len(parts)-1]
				if strings.HasPrefix(possibleID, "m") {
					id = possibleID
				}
			}
		}
		// Mercari Shops 商品: /shops/product/XYZ
		if strings.Contains(itemURL, "/shops/product/") {
			parts := strings.Split(itemURL, "/")
			if len(parts) > 0 {
				id = "shops_" + parts[len(parts)-1]
			}
		}
	}

	// 解析价格。
	priceEl, err := el.Element(".merPrice")
	if err != nil {
		return nil, fmt.Errorf("price: %w", err)
	}
	priceTxt, err := priceEl.Text()
	if err != nil {
		return nil, fmt.Errorf("price text: %w", err)
	}
	priceVal, err := parsePrice(priceTxt)
	if err != nil {
		return nil, fmt.Errorf("parse price: %w", err)
	}

	imageURL := ""
	if src != nil {
		imageURL = *src
	}
	itemURL = normalizeMercariURL(itemURL)

	return &pb.Item{
		SourceId: id,
		Title:    titleStr,
		Price:    int32(priceVal),
		ImageUrl: imageURL,
		ItemUrl:  itemURL,
		Status:   "on_sale",
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
	cleaned := strings.ReplaceAll(txt, "¥", "")
	cleaned = strings.ReplaceAll(cleaned, ",", "")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return 0, fmt.Errorf("empty price")
	}
	match := priceRe.FindString(cleaned)
	if match == "" {
		return 0, fmt.Errorf("no digits")
	}
	val, err := strconv.ParseInt(match, 10, 64)
	if err != nil {
		return 0, err
	}
	return val, nil
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

	if err := s.browser.Close(); err != nil {
		s.logger.Error("close browser failed", slog.String("error", err.Error()))
	} else {
		metrics.CrawlerBrowserInstances.Dec()
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
