package crawler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/go-rod/rod"
)

// 页面检测关键词
var (
	noItemsHints = []string{
		"出品された商品がありません",
		"該当する商品はありません",
		"検索結果はありません",
		"商品が見つかりません",
		"見つかりませんでした",
		"検索結果がありません",
	}
	blockedHints = []string{
		"cloudflare",
		"attention required",
		"verify you are human",
		"access denied",
		"temporarily unavailable",
		"just a moment",
		"checking your browser",
		"challenge-platform",
		"cf-browser-verification",
		"recaptcha",
		"hcaptcha",
		"captcha",
		"403 forbidden",
		"429 too many requests",
		"blocked",
		"rate limited",
		"too many requests",
		"err_connection",
		"err_proxy",
		"proxy error",
	}
)

// containsAny 检查文本是否包含任意一个关键词
func containsAny(text string, keywords []string) bool {
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

// getPageBodyText 获取页面 body 文本（带超时保护）
func (s *Service) getPageBodyText(page *rod.Page) string {
	pWithTimeout := page.Timeout(pageTextCheckTimeout)
	body, err := pWithTimeout.Element("body")
	if err != nil {
		return ""
	}
	text, err := body.Text()
	if err != nil {
		return ""
	}
	return text
}

func (s *Service) isNoItemsPage(page *rod.Page) bool {
	// 先检查空状态 DOM 元素
	if elems, err := page.Elements(".merEmptyState"); err == nil && len(elems) > 0 {
		return true
	}
	// 再检查页面文本
	text := s.getPageBodyText(page)
	return text != "" && containsAny(text, noItemsHints)
}

func (s *Service) isBlockedPage(page *rod.Page) bool {
	// 使用独立的 context 进行检测，不受任务 context 影响
	diagCtx, diagCancel := context.WithTimeout(context.Background(), pageTextCheckTimeout)
	defer diagCancel()
	diagPage := page.Context(diagCtx)

	// 1. 检查页面标题是否为 about:blank（代理/网络问题的常见表现）
	if info, err := diagPage.Info(); err == nil {
		title := strings.ToLower(info.Title)
		url := strings.ToLower(info.URL)

		// about:blank 页面通常表示代理或网络问题
		if title == "about:blank" || title == "" {
			s.logger.Debug("detected blank page title",
				slog.String("title", info.Title),
				slog.String("url", info.URL))
			// 如果 URL 也是 about:blank，这是明确的空白页
			if url == "about:blank" || url == "" {
				return true
			}
		}

		// 检查标题中的封锁特征
		blockedTitles := []string{
			"just a moment",
			"attention required",
			"access denied",
			"403 forbidden",
			"error",
			"blocked",
		}
		for _, blocked := range blockedTitles {
			if strings.Contains(title, blocked) {
				s.logger.Debug("detected blocked page title",
					slog.String("title", info.Title),
					slog.String("blocked_hint", blocked))
				return true
			}
		}
	}

	// 2. 检查页面内容
	text := s.getPageBodyText(page)

	// 如果页面内容非常少，可能是空白页或加载失败
	if len(text) < 50 {
		s.logger.Debug("detected very short page content",
			slog.Int("content_length", len(text)))
		return true
	}

	// 3. 检查页面内容中的封锁关键词
	return containsAny(strings.ToLower(text), blockedHints)
}

// detectBlockType 检测页面被拦截的类型
func (s *Service) detectBlockType(title, html string) string {
	lowerTitle := strings.ToLower(title)
	lowerHTML := strings.ToLower(html)

	// Cloudflare 拦截（增强检测）
	if strings.Contains(lowerTitle, "just a moment") ||
		strings.Contains(lowerHTML, "cloudflare") ||
		strings.Contains(lowerHTML, "cf-browser-verification") ||
		strings.Contains(lowerHTML, "challenge-platform") ||
		strings.Contains(lowerHTML, `iframe`) && strings.Contains(lowerHTML, "challenges.cloudflare.com") ||
		strings.Contains(lowerHTML, `id="challenge-form"`) ||
		strings.Contains(lowerHTML, `id="challenge-running"`) ||
		strings.Contains(lowerHTML, `id="challenge-stage"`) ||
		strings.Contains(lowerHTML, "turnstile") { // Cloudflare Turnstile
		return "cloudflare_challenge"
	}

	// 人机验证
	if strings.Contains(lowerHTML, "captcha") ||
		strings.Contains(lowerHTML, "recaptcha") ||
		strings.Contains(lowerHTML, "hcaptcha") ||
		strings.Contains(lowerHTML, "verify you are human") {
		return "captcha"
	}

	// 403 Forbidden（IP 被封）
	if strings.Contains(lowerTitle, "403") ||
		strings.Contains(lowerTitle, "forbidden") ||
		strings.Contains(lowerHTML, "access denied") ||
		strings.Contains(lowerHTML, "403 error") {
		return "403_forbidden"
	}

	// 429 Too Many Requests（速率限制）
	if strings.Contains(lowerTitle, "429") ||
		strings.Contains(lowerHTML, "too many requests") ||
		strings.Contains(lowerHTML, "rate limit") {
		return "429_rate_limited"
	}

	// 完全空白页
	if title == "" || title == "about:blank" {
		if len(html) < 100 || strings.Contains(html, "<html><head></head><body></body></html>") {
			return "blank_page"
		}
		return "empty_title"
	}

	// 连接错误
	if strings.Contains(lowerHTML, "err_connection") ||
		strings.Contains(lowerHTML, "err_proxy") ||
		strings.Contains(lowerHTML, "proxy error") {
		return "connection_error"
	}

	return "unknown"
}

// detectBlockTypeFromPage 从页面 DOM 检测拦截类型（更精确的检测）
func (s *Service) detectBlockTypeFromPage(ctx context.Context, page *rod.Page) string {
	detectCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	p := page.Context(detectCtx)

	// 1. 检测 Cloudflare iframe
	if iframe, err := p.Element(`iframe[src*="cloudflare"], iframe[src*="challenges"]`); err == nil && iframe != nil {
		return "cloudflare_challenge"
	}

	// 2. 检测 challenge-form（Cloudflare JS Challenge）
	if form, err := p.Element(`#challenge-form, #challenge-running, [id*="challenge"]`); err == nil && form != nil {
		return "cloudflare_challenge"
	}

	// 3. 检测 Turnstile widget
	if turnstile, err := p.Element(`[class*="turnstile"], .cf-turnstile`); err == nil && turnstile != nil {
		return "cloudflare_challenge"
	}

	// 4. 检测 CAPTCHA 元素
	if captcha, err := p.Element(`[class*="captcha"], [id*="captcha"], .g-recaptcha, .h-captcha`); err == nil && captcha != nil {
		return "captcha"
	}

	// 5. 检测错误页面特征（有 header 但没有商品列表）
	hasHeader, _ := p.Element(`header, [data-testid="header"]`)
	hasItemCell, _ := p.Element(`[data-testid="item-cell"]`)
	hasEmptyState, _ := p.Element(`.merEmptyState`)

	// 页面框架存在，但既没有商品也没有空状态提示 = 可能是 JS 挑战或动态加载问题
	if hasHeader != nil && hasItemCell == nil && hasEmptyState == nil {
		// 检查是否有加载中的状态
		if loading, err := p.Element(`[class*="loading"], [class*="Loading"], [class*="skeleton"]`); err == nil && loading != nil {
			return "js_loading_stuck"
		}
		return "js_challenge_suspected"
	}

	return ""
}

// logPageTimeout 记录页面超时日志
func (s *Service) logPageTimeout(phase string, taskID string, url string, page *rod.Page, err error) {
	readyState := "unknown"
	pageTitle := "unknown"
	pageHTML := ""
	screenshotPath := ""

	if page != nil {
		// 使用独立的 context 进行诊断操作
		// 即使任务 context 已超时，诊断操作仍能成功执行
		diagCtx, diagCancel := context.WithTimeout(context.Background(), debugHTMLTimeout)
		defer diagCancel()

		// 使用带超时的页面进行诊断
		diagPage := page.Context(diagCtx)

		// 获取 readyState
		if v, evalErr := diagPage.Eval("document.readyState"); evalErr == nil {
			if state := v.Value.String(); state != "" {
				readyState = state
			}
		}

		// 获取页面标题
		if v, evalErr := diagPage.Eval("document.title"); evalErr == nil {
			if title := v.Value.String(); title != "" {
				pageTitle = title
			}
		}

		// 获取页面 HTML 片段（前 2000 字符用于诊断）
		if v, evalErr := diagPage.Eval("document.documentElement.outerHTML.substring(0, 2000)"); evalErr == nil {
			pageHTML = v.Value.String()
		}

		// 保存截图用于诊断（使用独立的超时）
		screenshotPath = s.saveDebugScreenshot(taskID, phase, page)
	}

	// 判断被拦截类型
	blockType := s.detectBlockType(pageTitle, pageHTML)

	s.logger.Warn("page timeout",
		slog.String("phase", phase),
		slog.String("task_id", taskID),
		slog.String("url", url),
		slog.Duration("timeout", s.pageTimeout),
		slog.String("ready_state", readyState),
		slog.String("page_title", pageTitle),
		slog.String("block_type", blockType),
		slog.String("screenshot", screenshotPath),
		slog.String("error", err.Error()))
}

// saveDebugScreenshot 保存调试截图，返回截图路径
// 需要通过配置 browser.debug_screenshot=true 或环境变量 BROWSER_DEBUG_SCREENSHOT=true 开启
func (s *Service) saveDebugScreenshot(taskID, phase string, page *rod.Page) string {
	// 检查配置开关
	if !s.cfg.Browser.DebugScreenshot {
		return ""
	}

	if page == nil {
		return ""
	}

	// 创建截图目录
	screenshotDir := "/tmp/goodshunter/screenshots"
	if err := os.MkdirAll(screenshotDir, 0755); err != nil {
		s.logger.Warn("failed to create screenshot directory",
			slog.String("dir", screenshotDir),
			slog.String("error", err.Error()))
		return ""
	}

	// 生成文件名：taskID_phase_timestamp.png
	timestamp := time.Now().Format("20060102_150405")
	filename := fmt.Sprintf("%s_%s_%s.png", taskID, phase, timestamp)
	filepath := fmt.Sprintf("%s/%s", screenshotDir, filename)

	// 设置截图超时（使用独立的 context，不依赖任务 context）
	screenshotCtx, cancel := context.WithTimeout(context.Background(), debugScreenshotTimeout)
	defer cancel()

	// 保存截图
	done := make(chan error, 1)
	go func() {
		data, err := page.Screenshot(false, nil)
		if err != nil {
			done <- err
			return
		}
		done <- os.WriteFile(filepath, data, 0644)
	}()

	select {
	case err := <-done:
		if err != nil {
			s.logger.Warn("failed to save screenshot",
				slog.String("task_id", taskID),
				slog.String("error", err.Error()))
			return ""
		}
		s.logger.Info("debug screenshot saved",
			slog.String("task_id", taskID),
			slog.String("path", filepath))
		return filepath
	case <-screenshotCtx.Done():
		s.logger.Warn("screenshot timeout",
			slog.String("task_id", taskID))
		return ""
	}
}

// ============================================================================
// 错误分类
// ============================================================================

// crawlErrorType 爬虫错误类型
type crawlErrorType int

const (
	errTypeUnknown crawlErrorType = iota
	errTypeTimeout
	errTypeBlocked    // 被封禁（403/429/Cloudflare等）
	errTypeNetwork    // 网络错误
	errTypeParseError // 解析错误
)

// classifyError 统一的错误分类函数
func classifyError(err error) crawlErrorType {
	if err == nil {
		return errTypeUnknown
	}

	// 先检查标准 context 错误
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return errTypeTimeout
	}

	msg := strings.ToLower(err.Error())

	// 检查被封禁的错误
	blockedKeywords := []string{
		"blocked_page", "cloudflare", "attention required",
		"access denied", "403", "429", "forbidden", "too many requests",
	}
	for _, kw := range blockedKeywords {
		if strings.Contains(msg, kw) {
			return errTypeBlocked
		}
	}

	// 检查超时错误
	if strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded") {
		return errTypeTimeout
	}

	// 检查网络错误
	networkKeywords := []string{"net::", "connection", "navigate"}
	for _, kw := range networkKeywords {
		if strings.Contains(msg, kw) {
			return errTypeNetwork
		}
	}

	// 检查解析错误
	if strings.Contains(msg, "parse") || strings.Contains(msg, "extract") {
		return errTypeParseError
	}

	return errTypeUnknown
}

// shouldActivateProxy 判断是否应该切换到代理模式
func shouldActivateProxy(err error) bool {
	if err == nil {
		return false
	}
	errType := classifyError(err)
	// 被封禁、超时、网络错误都应该尝试代理
	return errType == errTypeBlocked || errType == errTypeTimeout || errType == errTypeNetwork
}

// classifyCrawlerError 返回用于 metrics 的错误类型字符串
func classifyCrawlerError(err error) string {
	switch classifyError(err) {
	case errTypeTimeout:
		return "timeout"
	case errTypeNetwork:
		return "network_error"
	case errTypeParseError:
		return "parse_error"
	case errTypeBlocked:
		return "blocked"
	default:
		return "unknown"
	}
}

// classifyCrawlStatus 返回用于 metrics 的爬取状态字符串
func classifyCrawlStatus(err error) string {
	if err == nil {
		return "success"
	}
	if classifyError(err) == errTypeBlocked {
		return "403_forbidden"
	}
	return "error"
}
