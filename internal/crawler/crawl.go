package crawler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"sync/atomic"
	"time"

	"goodshunter/internal/pkg/metrics"
	"goodshunter/internal/pkg/redisqueue"
	"goodshunter/proto/pb"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
)

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
			// 设置为比任务超时稍长，给正常超时一点余量
			done := make(chan struct{})

			// 看门狗 goroutine
			// 注意：看门狗只负责记录日志和 watchdog 特定指标，不更新 TotalFailed
			// 因为任务最终会通过 context 超时返回，届时 FetchItems 会正确更新统计
			// 这样避免同一任务被重复计数
			go func() {
				select {
				case <-done:
					// 任务正常完成
				case <-time.After(watchdogTimeout):
					// 看门狗超时触发 - 仅记录日志和 watchdog 专用指标
					s.logger.Error("watchdog timeout triggered, task stuck",
						slog.String("task_id", taskID),
						slog.Duration("elapsed", time.Since(taskStart)))
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
					pushCtx, pushCancel := context.WithTimeout(context.Background(), redisOperationTimeout)
					defer pushCancel()
					if pushErr := s.redisQueue.PushResult(pushCtx, resp); pushErr != nil {
						s.logger.Error("push panic result failed", slog.String("error", pushErr.Error()))
					}
				}
			}()

			// 为每个任务设置独立的上下文
			taskCtx, cancel := context.WithTimeout(context.Background(), taskTimeout)
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

			// 标记任务是否真正完成（用于决定是否调用 AckTask）
			taskCompleted := false

			select {
			case result := <-resultCh:
				resp = result.resp
				err = result.err
				taskCompleted = true // 任务真正完成了（无论成功或失败）
			case <-taskCtx.Done():
				err = fmt.Errorf("task context timeout: %w", taskCtx.Err())
				s.logger.Error("task context timeout",
					slog.String("task_id", taskID),
					slog.Duration("elapsed", time.Since(taskStart)))
				// 超时时 taskCompleted = false，不调用 AckTask
				// 任务会留在 processing queue，由 Janitor 来 rescue
				// 同时 pending set 保持 taskID，防止调度器重复推送
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
				pushCtx, pushCancel := context.WithTimeout(context.Background(), redisOperationTimeout)
				defer pushCancel()

				if pushErr := s.redisQueue.PushResult(pushCtx, resp); pushErr != nil {
					s.logger.Error("push redis result failed", slog.String("error", pushErr.Error()))
				}
			}

			// 只有任务真正完成时才调用 AckTask
			// 超时的任务会留在 processing queue，由 Janitor 来处理
			if taskCompleted {
				ackCtx, ackCancel := context.WithTimeout(context.Background(), redisOperationTimeout)
				defer ackCancel()
				if ackErr := s.redisQueue.AckTask(ackCtx, t); ackErr != nil {
					s.logger.Error("failed to ack task",
						slog.String("task_id", taskID),
						slog.String("error", ackErr.Error()))
				} else {
					s.logger.Debug("task acked", slog.String("task_id", taskID))
				}
			} else {
				s.logger.Warn("task not acked (timeout), will be rescued by janitor",
					slog.String("task_id", taskID))
			}
		}(task)
	}
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
				s.logger.Debug("restart signal already pending, skipping",
					slog.Uint64("count", newCount))
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
	currentMode := "direct"
	if s.isUsingProxy() {
		currentMode = "proxy"
	}
	s.logger.Debug("doCrawl started",
		slog.String("task_id", taskID),
		slog.Int("attempt", attempt),
		slog.String("mode", currentMode))

	// 使用独立的 context 确保浏览器状态检查不受任务 ctx 影响
	// 这对于任务超时后需要切换代理模式的场景至关重要
	browserStateCtx, browserStateCancel := context.WithTimeout(context.Background(), pageCreateTimeout)
	defer browserStateCancel()
	if err := s.ensureBrowserState(browserStateCtx); err != nil {
		s.logger.Warn("ensureBrowserState failed", slog.String("task_id", taskID), slog.String("error", err.Error()))
		return nil, fmt.Errorf("ensure browser state: %w", err)
	}

	// 确认实际使用的模式（可能在 ensureBrowserState 中发生切换）
	actualMode := "direct"
	if s.isUsingProxy() {
		actualMode = "proxy"
	}
	s.logger.Debug("browser state ensured",
		slog.String("task_id", taskID),
		slog.String("actual_mode", actualMode))

	if attempt == 0 && !s.isUsingProxy() && atomic.CompareAndSwapUint32(&s.forceProxyOnce, 1, 0) {
		s.logger.Warn("forcing proxy activation due to FORCE_PROXY_ONCE env",
			slog.String("reason", "forced"),
			slog.Duration("cooldown", s.cfg.App.ProxyCooldown))
		if err := s.setProxyCooldown(ctx, s.cfg.App.ProxyCooldown); err != nil {
			return nil, err
		}
		s.logger.Info("proxy cooldown set, retrying with proxy mode",
			slog.String("task_id", taskID))
		return s.doCrawl(ctx, req, attempt+1)
	}

	response, err := s.crawlOnce(ctx, req)
	if err == nil {
		// 成功时重置连续失败计数（Redis 同步）
		s.resetConsecutiveFailures()
		return response, nil
	}

	// 增加连续失败计数（Redis 原子操作，多实例同步）
	failureCount := s.incrConsecutiveFailures()
	s.logger.Debug("consecutive failure recorded (Redis)",
		slog.String("task_id", taskID),
		slog.Int64("failure_count", failureCount),
		slog.Int("threshold", s.proxyFailureThreshold))

	// 只有在启用自动代理切换、直连模式下、且连续失败达到阈值后，才考虑切换到代理
	if s.proxyAutoSwitch && attempt == 0 && !s.isUsingProxy() && shouldActivateProxy(err) {
		if int(failureCount) < s.proxyFailureThreshold {
			// 未达到阈值，不切换代理，直接返回错误
			s.logger.Info("direct connection failed, but threshold not reached",
				slog.String("task_id", taskID),
				slog.Int64("failure_count", failureCount),
				slog.Int("threshold", s.proxyFailureThreshold),
				slog.String("error", err.Error()))
			return nil, err
		}

		s.logger.Warn("direct connection failed, consecutive failures reached threshold, checking proxy",
			slog.Int64("failure_count", failureCount),
			slog.Int("threshold", s.proxyFailureThreshold),
			slog.Duration("cooldown", s.cfg.App.ProxyCooldown),
			slog.String("trigger_error", err.Error()),
			slog.String("error_type", classifyCrawlerError(err)))

		// 先进行代理健康检查（使用独立 context，不受任务 ctx 超时影响）
		healthCtx, healthCancel := context.WithTimeout(context.Background(), 30*time.Second)
		proxyHealthy := s.checkProxyHealthWithRetry(healthCtx, 2)
		healthCancel()

		if !proxyHealthy {
			s.logger.Warn("proxy health check failed, staying in direct mode",
				slog.String("task_id", taskID),
				slog.String("hint", "check proxy configuration or consider using residential proxy"))
			metrics.CrawlerErrorsTotal.WithLabelValues("internal", "proxy_unhealthy").Inc()
			// 代理不可用，不切换模式，直接返回原始错误
			return nil, fmt.Errorf("direct failed and proxy unhealthy: %w", err)
		}

		s.logger.Info("proxy health check passed, activating proxy mode",
			slog.String("task_id", taskID))

		// 重置失败计数（切换代理后重新开始计数，Redis 同步）
		s.resetConsecutiveFailures()

		if setErr := s.setProxyCooldown(ctx, s.cfg.App.ProxyCooldown); setErr != nil {
			s.logger.Error("failed to set proxy cooldown", slog.String("error", setErr.Error()))
		}

		// 检查 ctx 是否已超时或即将超时
		// 如果 ctx 剩余时间不足，不要重试当前任务，而是让它重新入队
		if ctx.Err() != nil {
			s.logger.Info("context already cancelled, skip retry and let task re-queue",
				slog.String("task_id", taskID))
			return nil, fmt.Errorf("proxy activated, task needs retry: %w", err)
		}

		// ctx 还有足够时间，可以尝试重试
		s.logger.Info("retrying with proxy mode", slog.String("task_id", taskID))
		return s.doCrawl(ctx, req, attempt+1)
	}

	return response, err
}

// handleBlockEvent 处理封锁事件：记录连续失败并可能触发休眠
func (s *Service) handleBlockEvent(ctx context.Context, taskID string, blockType string) {
	if s.adaptiveThrottler == nil {
		return
	}

	// 如果是 403，清除 Cookie 缓存（可能已失效）
	if blockType == "403_forbidden" {
		if s.cookieManager != nil {
			s.cookieManager.ClearCookies(ctx)
		}
	}

	// 记录封锁事件
	shouldSleep, sleepDuration := s.adaptiveThrottler.RecordBlock(ctx, taskID, blockType)

	// 如果需要休眠（连续失败过多）
	if shouldSleep {
		s.logger.Warn("adaptive throttle: entering cooldown sleep",
			slog.String("task_id", taskID),
			slog.Duration("duration", sleepDuration))

		select {
		case <-time.After(sleepDuration):
			s.logger.Info("adaptive throttle: cooldown completed",
				slog.String("task_id", taskID))
		case <-ctx.Done():
			s.logger.Debug("adaptive throttle: cooldown interrupted by context",
				slog.String("task_id", taskID))
		}

		s.adaptiveThrottler.ExitCooldown()
	}
}

// crawlOnce 执行单次爬取逻辑（不包含自动切换与重试）。
func (s *Service) crawlOnce(ctx context.Context, req *pb.FetchRequest) (*pb.FetchResponse, error) {
	taskID := req.GetTaskId()
	crawlStart := time.Now()
	crawlMode := "direct"
	if s.isUsingProxy() {
		crawlMode = "proxy"
	}
	s.logger.Debug("crawlOnce started",
		slog.String("task_id", taskID),
		slog.String("mode", crawlMode))

	if s.rateLimiter != nil {
		s.logger.Debug("waiting for rate limit", slog.String("task_id", taskID))
		rateLimitStart := time.Now()
		// 为速率限制设置最大等待时间，防止无限等待
		rateLimitDeadline := time.After(rateLimitMaxWait)
	RateLimitLoop:
		for {
			// 为单次 Redis 调用设置短超时
			rateLimitCtx, rateLimitCancel := context.WithTimeout(ctx, rateLimitCheckTimeout)
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
				break RateLimitLoop
			case <-time.After(50 * time.Millisecond):
			}
		}
	}

	// 添加随机延迟（Jitter）降低被识别为爬虫的风险
	// 仅在直连模式下使用，代理模式下跳过（代理本身已提供一定程度的保护）
	if !s.isUsingProxy() {
		jitter := jitterMinDelay + time.Duration(rand.Int63n(int64(jitterMaxDelay-jitterMinDelay)))
		s.logger.Debug("applying request jitter",
			slog.String("task_id", taskID),
			slog.Duration("jitter", jitter))

		select {
		case <-time.After(jitter):
			// 延迟完成
		case <-ctx.Done():
			return nil, fmt.Errorf("context cancelled during jitter: %w", ctx.Err())
		}
	}

	url := BuildMercariURL(req)
	s.logger.Debug("built mercari URL", slog.String("task_id", taskID), slog.String("url", url))

	// 使用实例解耦：获取浏览器引用并注册活跃页面
	// 这样即使在任务执行期间浏览器被切换，当前任务仍然使用旧浏览器完成
	browser, allowed := s.trackPageStart()
	if !allowed {
		// 正在排水中，拒绝新任务
		return nil, fmt.Errorf("browser draining, task needs retry")
	}
	if browser == nil {
		return nil, fmt.Errorf("browser not initialized")
	}
	// 确保任务结束时减少活跃页面计数
	defer s.trackPageEnd()

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

	// 页面创建的超时保护 - 只用于这个 select，不影响页面对象的内部 context
	pageCreateTimer := time.NewTimer(pageCreateTimeout)
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
		return nil, fmt.Errorf("create page timeout after %v", pageCreateTimeout)
	case <-ctx.Done():
		return nil, fmt.Errorf("context cancelled during page creation: %w", ctx.Err())
	}

	// Stealth 脚本应用 - 同样只用 select 做超时保护
	stealthTimer := time.NewTimer(stealthScriptTimeout)
	defer stealthTimer.Stop()
	stealthDone := make(chan error, 1)
	go func() {
		// 1. 应用 go-rod/stealth 的基础脚本
		_, evalErr := basePage.EvalOnNewDocument(stealth.JS)
		if evalErr != nil {
			stealthDone <- evalErr
			return
		}

		// 2. 应用增强的反检测脚本
		_, evalErr = basePage.EvalOnNewDocument(enhancedStealthJS)
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
		return nil, fmt.Errorf("apply stealth script timeout after %v", stealthScriptTimeout)
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
		// "*line-scdn*", // 注释掉：LINE 是 Mercari 母公司，可能托管关键 JS/API
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

	// 设置超时与 UA
	page = page.Timeout(s.pageTimeout)
	if err := page.SetUserAgent(&proto.NetworkSetUserAgentOverride{UserAgent: s.defaultUA}); err != nil {
		s.logger.Warn("set user agent failed", slog.String("task_id", taskID), slog.String("error", err.Error()))
	}

	// 加载缓存的 Cookie（降低触发挑战的频率）
	if s.cookieManager != nil {
		if err := s.cookieManager.LoadCookies(ctx, page, taskID); err != nil {
			s.logger.Debug("load cookies failed",
				slog.String("task_id", taskID),
				slog.String("error", err.Error()))
		}
	}

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

	// 等待页面加载完成（DOM + 资源）
	loadCtx, loadCancel := context.WithTimeout(ctx, 30*time.Second)
	defer loadCancel()
	if err := page.Context(loadCtx).WaitLoad(); err != nil {
		s.logger.Warn("WaitLoad failed, continuing anyway",
			slog.String("task_id", taskID),
			slog.String("error", err.Error()))
	}

	// 等待网络请求完成（API 数据加载）
	// WaitRequestIdle 返回一个等待函数，调用它会阻塞直到网络空闲
	waitIdle := page.WaitRequestIdle(1*time.Second, nil, nil, nil)
	idleCtx, idleCancel := context.WithTimeout(ctx, 15*time.Second)
	defer idleCancel()
	idleDone := make(chan struct{})
	go func() {
		waitIdle()
		close(idleDone)
	}()
	select {
	case <-idleDone:
		s.logger.Debug("network idle reached", slog.String("task_id", taskID))
	case <-idleCtx.Done():
		s.logger.Debug("WaitRequestIdle timeout, continuing", slog.String("task_id", taskID))
	}

	// 模拟人类行为：随机鼠标移动和滚动（降低被识别为爬虫的风险）
	// 在检查封锁前执行，因为某些反爬系统会检测行为模式
	s.simulateHumanBehavior(ctx, page, taskID)

	info, err := page.Info()
	if err == nil {
		s.logger.Info("page loaded",
			slog.String("task_id", taskID),
			slog.String("title", info.Title),
			slog.String("actual_url", info.URL),
		)

		// 早期检测：如果导航后页面是 about:blank，说明有网络/代理问题
		if info.Title == "about:blank" || info.Title == "" {
			blockType := s.detectBlockType(info.Title, "")
			s.logger.Warn("detected blank page after navigation",
				slog.String("task_id", taskID),
				slog.String("title", info.Title),
				slog.String("url", info.URL),
				slog.String("target_url", url),
				slog.String("block_type", blockType))

			// 保存截图用于诊断
			s.saveDebugScreenshot(taskID, "blank_page", page)

			// 记录封锁事件并检查是否需要休眠
			s.handleBlockEvent(ctx, taskID, blockType)

			return nil, fmt.Errorf("blocked_page: %s", blockType)
		}
	}

	// 注意：封锁检测已移至 Race() 超时后执行
	// 原因：Mercari 是 React SPA，WaitLoad/WaitRequestIdle 完成后 React 可能还在渲染
	// 过早检测会导致误判，应该先给页面足够时间加载商品

	// 只做早期的"明确封锁"检测（Cloudflare iframe/challenge-form 等）
	// 这些特征即使在页面未完全加载时也能准确识别
	if domBlockType := s.detectBlockTypeFromPage(ctx, page); domBlockType != "" {
		s.logger.Warn("detected blocked page via DOM inspection",
			slog.String("task_id", taskID),
			slog.String("block_type", domBlockType))
		s.saveDebugScreenshot(taskID, "blocked_"+domBlockType, page)
		s.handleBlockEvent(ctx, taskID, domBlockType)
		return nil, fmt.Errorf("blocked_page: %s", domBlockType)
	}

	// 调试：检查页面关键元素是否存在，帮助诊断空 Grid 问题
	if s.cfg.Browser.DebugScreenshot {
		debugCtx, debugCancel := context.WithTimeout(ctx, 5*time.Second)
		// 检查页面结构
		hasHeader, _ := page.Context(debugCtx).Element(`header, [data-testid="header"]`)
		hasSidebar, _ := page.Context(debugCtx).Element(`aside, [class*="sidebar"], [class*="Sidebar"]`)
		hasGrid, _ := page.Context(debugCtx).Element(`[data-testid="search-result"], [class*="SearchResult"]`)
		hasItemCell, _ := page.Context(debugCtx).Element(`[data-testid="item-cell"]`)
		debugCancel()

		s.logger.Debug("page structure check",
			slog.String("task_id", taskID),
			slog.Bool("has_header", hasHeader != nil),
			slog.Bool("has_sidebar", hasSidebar != nil),
			slog.Bool("has_grid", hasGrid != nil),
			slog.Bool("has_item_cell", hasItemCell != nil))

		// 如果有 Grid 容器但没有商品，保存截图
		if hasGrid != nil && hasItemCell == nil {
			s.logger.Warn("grid container exists but no items - possible dynamic loading issue",
				slog.String("task_id", taskID))
			s.saveDebugScreenshot(taskID, "empty_grid", page)
		}
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
			// 新增：检测可能的反爬虫页面
			Element(`[class*="challenge"], [id*="challenge"]`).Handle(func(e *rod.Element) error {
			return fmt.Errorf("challenge_page")
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
		// 检测到反爬虫挑战页面
		if err.Error() == "challenge_page" {
			s.logger.Warn("detected challenge page (anti-bot)",
				slog.String("task_id", taskID),
				slog.String("url", url))
			s.saveDebugScreenshot(taskID, "challenge_page", page)
			return nil, fmt.Errorf("blocked_page: challenge")
		}

		// Race 超时/失败后，再检测是否被封锁
		// 此时页面已有足够时间加载，如果还没商品，可能确实被封锁了
		if s.isBlockedPage(page) {
			blockType := "unknown"
			if info != nil {
				blockType = s.detectBlockType(info.Title, s.getPageBodyText(page))
			}
			s.logger.Warn("detected blocked page after race timeout",
				slog.String("task_id", taskID),
				slog.String("block_type", blockType))
			s.saveDebugScreenshot(taskID, "blocked_"+blockType, page)
			s.handleBlockEvent(ctx, taskID, blockType)
			return nil, fmt.Errorf("blocked_page: %s", blockType)
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
		countCtx, countCancel := context.WithTimeout(ctx, elementCountTimeout)
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
ScrollLoop:
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
			break ScrollLoop // 超时跳出循环
		default:
			time.Sleep(scrollWaitInterval) // 等待页面渲染
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

	// 解析商品列表
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

	// 成功抓取后保存 Cookie（供后续请求复用）
	if s.cookieManager != nil && len(items) > 0 {
		if err := s.cookieManager.SaveCookies(ctx, page, taskID); err != nil {
			s.logger.Debug("save cookies failed",
				slog.String("task_id", taskID),
				slog.String("error", err.Error()))
		}
	}

	// 记录成功，重置连续失败计数
	if s.adaptiveThrottler != nil {
		s.adaptiveThrottler.RecordSuccess(ctx)
	}

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
