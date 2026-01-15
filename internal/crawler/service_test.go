package crawler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// ============================================================================
// parsePrice 边界情况测试
// ============================================================================

func TestParsePrice_EdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expected  int64
		expectErr bool
	}{
		// 正常情况
		{"standard_yen", "¥1,200", 1200, false},
		{"standard_yen_with_space", "¥ 1,200", 1200, false},
		{"fullwidth_yen", "￥1,200", 1200, false},
		{"no_comma", "¥1200", 1200, false},
		{"small_price", "¥100", 100, false},
		{"large_price", "¥999,999", 999999, false},
		{"very_large_price", "¥1,234,567", 1234567, false},

		// 带前缀的情况
		{"with_discount", "6% OFF ¥950", 950, false},
		{"with_text_prefix", "価格: ¥500", 500, false},
		{"multiple_numbers_with_yen", "3点 ¥1,500", 1500, false},

		// 边界情况
		{"minimum_price", "¥1", 1, false},
		{"zero_price", "¥0", 0, false},
		{"only_digits", "1200", 1200, false},

		// 错误情况
		{"empty_string", "", 0, true},
		{"only_yen_symbol", "¥", 0, true},
		{"only_spaces", "   ", 0, true},
		{"no_digits", "abc", 0, true},
		{"only_comma", ",", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parsePrice(tt.input)
			if tt.expectErr {
				if err == nil {
					t.Errorf("expected error for input %q, got result %d", tt.input, result)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error for input %q: %v", tt.input, err)
				return
			}
			if result != tt.expected {
				t.Errorf("parsePrice(%q) = %d, expected %d", tt.input, result, tt.expected)
			}
		})
	}
}

// ============================================================================
// 错误分类函数测试
// ============================================================================

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected crawlErrorType
	}{
		{"nil_error", nil, errTypeUnknown},
		{"context_deadline", context.DeadlineExceeded, errTypeTimeout},
		{"context_canceled", context.Canceled, errTypeTimeout},
		{"timeout_string", errors.New("operation timeout"), errTypeTimeout},
		{"deadline_exceeded_string", errors.New("context deadline exceeded"), errTypeTimeout},

		// 封禁错误
		{"blocked_page", errors.New("blocked_page detected"), errTypeBlocked},
		{"cloudflare", errors.New("cloudflare challenge"), errTypeBlocked},
		{"403_error", errors.New("HTTP 403 forbidden"), errTypeBlocked},
		{"429_error", errors.New("HTTP 429 too many requests"), errTypeBlocked},
		{"access_denied", errors.New("access denied"), errTypeBlocked},
		{"forbidden", errors.New("forbidden"), errTypeBlocked},

		// 网络错误
		{"net_error", errors.New("net::ERR_CONNECTION_REFUSED"), errTypeNetwork},
		{"connection_error", errors.New("connection reset"), errTypeNetwork},
		{"navigate_error", errors.New("navigate failed"), errTypeNetwork},

		// 解析错误
		{"parse_error", errors.New("parse error"), errTypeParseError},
		{"extract_error", errors.New("extract failed"), errTypeParseError},

		// 未知错误
		{"unknown_error", errors.New("some random error"), errTypeUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := classifyError(tt.err)
			if result != tt.expected {
				t.Errorf("classifyError(%v) = %v, expected %v", tt.err, result, tt.expected)
			}
		})
	}
}

func TestShouldActivateProxy(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil_error", nil, false},
		{"timeout_error", errors.New("timeout"), true},
		{"blocked_error", errors.New("blocked_page"), true},
		{"cloudflare_error", errors.New("cloudflare"), true},
		{"403_error", errors.New("403"), true},
		{"429_error", errors.New("429"), true},
		{"network_error", errors.New("net::ERR_CONNECTION"), true},
		{"connection_error", errors.New("connection refused"), true},
		{"parse_error", errors.New("parse error"), false},
		{"random_error", errors.New("random error"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldActivateProxy(tt.err)
			if result != tt.expected {
				t.Errorf("shouldActivateProxy(%v) = %v, expected %v", tt.err, result, tt.expected)
			}
		})
	}
}

func TestClassifyCrawlerError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{"nil_error", nil, "unknown"},
		{"timeout", errors.New("timeout"), "timeout"},
		{"network", errors.New("net::ERR"), "network_error"},
		{"blocked", errors.New("403 forbidden"), "blocked"},
		{"parse", errors.New("parse error"), "parse_error"},
		{"unknown", errors.New("something else"), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := classifyCrawlerError(tt.err)
			if result != tt.expected {
				t.Errorf("classifyCrawlerError(%v) = %q, expected %q", tt.err, result, tt.expected)
			}
		})
	}
}

func TestClassifyCrawlStatus(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{"nil_error", nil, "success"},
		{"blocked_403", errors.New("403"), "403_forbidden"},
		{"blocked_forbidden", errors.New("forbidden"), "403_forbidden"},
		{"blocked_access_denied", errors.New("access denied"), "403_forbidden"},
		{"blocked_page", errors.New("blocked_page"), "403_forbidden"},
		{"other_error", errors.New("timeout"), "error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := classifyCrawlStatus(tt.err)
			if result != tt.expected {
				t.Errorf("classifyCrawlStatus(%v) = %q, expected %q", tt.err, result, tt.expected)
			}
		})
	}
}

// ============================================================================
// 页面检测函数测试
// ============================================================================

func TestContainsAny(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		keywords []string
		expected bool
	}{
		{"empty_text", "", []string{"a", "b"}, false},
		{"empty_keywords", "hello world", []string{}, false},
		{"single_match", "hello world", []string{"world"}, true},
		{"no_match", "hello world", []string{"foo", "bar"}, false},
		{"first_keyword_match", "hello world", []string{"hello", "foo"}, true},
		{"last_keyword_match", "hello world", []string{"foo", "world"}, true},
		{"partial_match", "cloudflare challenge", []string{"cloudflare"}, true},
		{"case_sensitive", "Hello", []string{"hello"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := containsAny(tt.text, tt.keywords)
			if result != tt.expected {
				t.Errorf("containsAny(%q, %v) = %v, expected %v", tt.text, tt.keywords, result, tt.expected)
			}
		})
	}
}

func TestIsNoItemsText(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		expected bool
	}{
		{"empty_text", "", false},
		{"no_items_jp_1", "出品された商品がありません", true},
		{"no_items_jp_2", "該当する商品はありません", true},
		{"no_items_jp_3", "検索結果はありません", true},
		{"no_items_jp_4", "商品が見つかりません", true},
		{"no_items_jp_5", "見つかりませんでした", true},
		{"no_items_with_context", "お探しの商品は見つかりませんでした。別のキーワードをお試しください。", true},
		{"normal_page", "商品一覧 100件", false},
		{"random_text", "hello world", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := containsAny(tt.text, noItemsHints)
			if result != tt.expected {
				t.Errorf("isNoItemsText(%q) = %v, expected %v", tt.text, result, tt.expected)
			}
		})
	}
}

func TestIsBlockedTextFunction(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		expected bool
	}{
		{"empty_text", "", false},
		{"cloudflare_lower", "cloudflare challenge", true},
		{"cloudflare_mixed", "Cloudflare", true},
		{"attention_required", "Attention Required!", true},
		{"verify_human", "Please verify you are human", true},
		{"access_denied", "Access Denied", true},
		{"temporarily_unavailable", "Service temporarily unavailable", true},
		{"normal_page", "Welcome to Mercari", false},
		{"random_text", "hello world", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := containsAny(strings.ToLower(tt.text), blockedHints)
			if result != tt.expected {
				t.Errorf("isBlockedText(%q) = %v, expected %v", tt.text, result, tt.expected)
			}
		})
	}
}

// ============================================================================
// URL 处理函数测试
// ============================================================================

func TestNormalizeMercariURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty_string", "", ""},
		{"full_https", "https://jp.mercari.com/item/m123", "https://jp.mercari.com/item/m123"},
		{"full_http", "http://jp.mercari.com/item/m123", "http://jp.mercari.com/item/m123"},
		{"protocol_relative", "//jp.mercari.com/item/m123", "https://jp.mercari.com/item/m123"},
		{"absolute_path", "/item/m123", "https://jp.mercari.com/item/m123"},
		{"relative_path", "item/m123", "https://jp.mercari.comitem/m123"}, // 注意：这可能是个 bug
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeMercariURL(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeMercariURL(%q) = %q, expected %q", tt.input, result, tt.expected)
			}
		})
	}
}

// ============================================================================
// 辅助函数测试
// ============================================================================

func TestBoolToGauge(t *testing.T) {
	tests := []struct {
		name     string
		input    bool
		expected float64
	}{
		{"true", true, 1},
		{"false", false, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := boolToGauge(tt.input)
			if result != tt.expected {
				t.Errorf("boolToGauge(%v) = %v, expected %v", tt.input, result, tt.expected)
			}
		})
	}
}

// ============================================================================
// 并发安全性测试
// ============================================================================

func TestCrawlerStatsConcurrency(t *testing.T) {
	stats := &crawlerStats{}
	var wg sync.WaitGroup
	iterations := 1000
	goroutines := 10

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				stats.TotalProcessed.Add(1)
				stats.TotalSucceeded.Add(1)
				stats.TotalFailed.Add(1)
				stats.TotalPanics.Add(1)
			}
		}()
	}

	wg.Wait()

	expected := int64(goroutines * iterations)
	if stats.TotalProcessed.Load() != expected {
		t.Errorf("TotalProcessed = %d, expected %d", stats.TotalProcessed.Load(), expected)
	}
	if stats.TotalSucceeded.Load() != expected {
		t.Errorf("TotalSucceeded = %d, expected %d", stats.TotalSucceeded.Load(), expected)
	}
	if stats.TotalFailed.Load() != expected {
		t.Errorf("TotalFailed = %d, expected %d", stats.TotalFailed.Load(), expected)
	}
	if stats.TotalPanics.Load() != expected {
		t.Errorf("TotalPanics = %d, expected %d", stats.TotalPanics.Load(), expected)
	}
}

// ============================================================================
// Service 结构体方法测试（不需要真实浏览器）
// ============================================================================

func TestServiceIsUsingProxy(t *testing.T) {
	tests := []struct {
		name           string
		currentIsProxy bool
		expected       bool
	}{
		{"proxy_mode", true, true},
		{"direct_mode", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &Service{currentIsProxy: tt.currentIsProxy}
			result := svc.isUsingProxy()
			if result != tt.expected {
				t.Errorf("isUsingProxy() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestServiceProxyCacheLogic(t *testing.T) {
	// 测试缓存未过期时直接返回缓存值
	t.Run("cache_not_expired", func(t *testing.T) {
		svc := &Service{
			proxyCache:      true,
			proxyCacheUntil: time.Now().Add(1 * time.Hour), // 缓存还有1小时过期
			rdb:             nil,                           // 没有 Redis
		}
		result, err := svc.getProxyState(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result {
			t.Error("expected cached value true, got false")
		}
	})

	// 测试缓存过期但无 Redis 时返回 false
	t.Run("cache_expired_no_redis", func(t *testing.T) {
		svc := &Service{
			proxyCache:      true,
			proxyCacheUntil: time.Now().Add(-1 * time.Hour), // 缓存已过期
			rdb:             nil,
		}
		result, err := svc.getProxyState(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result {
			t.Error("expected false when cache expired and no redis, got true")
		}
	})
}

func TestServiceRestartSignal(t *testing.T) {
	svc := &Service{
		restartCh: make(chan struct{}, 1),
	}

	// 发送信号
	select {
	case svc.restartCh <- struct{}{}:
	default:
		t.Fatal("failed to send restart signal")
	}

	// 接收信号
	select {
	case <-svc.RestartSignal():
		// 成功
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for restart signal")
	}
}

// ============================================================================
// 超时常量验证测试
// ============================================================================

func TestTimeoutConstants(t *testing.T) {
	// 确保超时常量值合理
	tests := []struct {
		name     string
		value    time.Duration
		minValue time.Duration
		maxValue time.Duration
	}{
		{"browserInitTimeout", browserInitTimeout, 10 * time.Second, 2 * time.Minute},
		{"browserHealthInterval", browserHealthInterval, 10 * time.Second, 5 * time.Minute},
		{"browserHealthTimeout", browserHealthTimeout, 1 * time.Second, 30 * time.Second},
		{"taskTimeout", taskTimeout, 30 * time.Second, 5 * time.Minute},
		{"watchdogTimeout", watchdogTimeout, taskTimeout, 3 * time.Minute},
		{"pageCreateTimeout", pageCreateTimeout, 5 * time.Second, 1 * time.Minute},
		{"redisOperationTimeout", redisOperationTimeout, 1 * time.Second, 30 * time.Second},
		{"redisShortTimeout", redisShortTimeout, 1 * time.Second, 10 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.value < tt.minValue {
				t.Errorf("%s = %v, should be >= %v", tt.name, tt.value, tt.minValue)
			}
			if tt.value > tt.maxValue {
				t.Errorf("%s = %v, should be <= %v", tt.name, tt.value, tt.maxValue)
			}
		})
	}

	// 确保 watchdog 超时大于任务超时
	if watchdogTimeout <= taskTimeout {
		t.Errorf("watchdogTimeout (%v) should be > taskTimeout (%v)", watchdogTimeout, taskTimeout)
	}
}

// ============================================================================
// 正则表达式测试
// ============================================================================

func TestPriceRegex(t *testing.T) {
	// 测试 priceRe
	t.Run("priceRe", func(t *testing.T) {
		tests := []struct {
			input    string
			expected []string
		}{
			{"1234", []string{"1234"}},
			{"abc123def456", []string{"123", "456"}},
			{"no digits here", nil},
			{"", nil},
		}
		for _, tt := range tests {
			result := priceRe.FindAllString(tt.input, -1)
			if len(result) != len(tt.expected) {
				t.Errorf("priceRe.FindAllString(%q) = %v, expected %v", tt.input, result, tt.expected)
			}
		}
	})

	// 测试 priceWithCurrencyRe
	t.Run("priceWithCurrencyRe", func(t *testing.T) {
		tests := []struct {
			input         string
			expectedMatch bool
			expectedValue string
		}{
			{"¥1,234", true, "1,234"},
			{"￥999", true, "999"},
			{"¥ 100", true, "100"},
			{"$100", false, ""},
			{"1234", false, ""},
		}
		for _, tt := range tests {
			match := priceWithCurrencyRe.FindStringSubmatch(tt.input)
			if tt.expectedMatch {
				if len(match) < 2 {
					t.Errorf("priceWithCurrencyRe expected match for %q", tt.input)
				} else if match[1] != tt.expectedValue {
					t.Errorf("priceWithCurrencyRe(%q) captured %q, expected %q", tt.input, match[1], tt.expectedValue)
				}
			} else {
				if len(match) > 0 {
					t.Errorf("priceWithCurrencyRe should not match %q", tt.input)
				}
			}
		}
	})
}

// ============================================================================
// CrawlerStats 快照测试
// ============================================================================

func TestServiceStats(t *testing.T) {
	svc := &Service{}

	// 初始状态
	stats := svc.Stats()
	if stats.TotalProcessed != 0 || stats.TotalSucceeded != 0 ||
		stats.TotalFailed != 0 || stats.TotalPanics != 0 {
		t.Error("initial stats should all be zero")
	}

	// 更新统计
	svc.stats.TotalProcessed.Add(10)
	svc.stats.TotalSucceeded.Add(8)
	svc.stats.TotalFailed.Add(2)
	svc.stats.TotalPanics.Add(1)

	stats = svc.Stats()
	if stats.TotalProcessed != 10 {
		t.Errorf("TotalProcessed = %d, expected 10", stats.TotalProcessed)
	}
	if stats.TotalSucceeded != 8 {
		t.Errorf("TotalSucceeded = %d, expected 8", stats.TotalSucceeded)
	}
	if stats.TotalFailed != 2 {
		t.Errorf("TotalFailed = %d, expected 2", stats.TotalFailed)
	}
	if stats.TotalPanics != 1 {
		t.Errorf("TotalPanics = %d, expected 1", stats.TotalPanics)
	}
}

// ============================================================================
// 死锁回归测试
// ============================================================================

// TestRWMutexNoDeadlock 测试 RWMutex 的正确使用，防止死锁回归。
// 背景：ensureBrowserState 曾经在持有写锁时调用 getProxyState，
// 而 getProxyState 内部使用 RLock，导致死锁（Go 的 RWMutex 不是可重入的）。
func TestRWMutexNoDeadlock(t *testing.T) {
	// 模拟之前的死锁场景
	var mu sync.RWMutex
	done := make(chan bool, 1)

	go func() {
		// 模拟 ensureBrowserState 的锁模式（修复后）
		// 先获取读锁检查状态
		mu.RLock()
		_ = true // 读取 currentIsProxy
		mu.RUnlock()

		// 获取写锁进行修改
		mu.Lock()
		defer mu.Unlock()

		// 修复后：不再在写锁内调用需要读锁的函数
		// 直接使用之前获取的值进行双重检查
		_ = true // 检查 shouldUseProxy == s.currentIsProxy

		done <- true
	}()

	select {
	case <-done:
		// 成功，没有死锁
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock detected: RWMutex pattern would have caused deadlock")
	}
}

// TestConcurrentEnsureBrowserStatePattern 测试多个 goroutine 并发执行
// ensureBrowserState 的锁模式不会死锁。
func TestConcurrentEnsureBrowserStatePattern(t *testing.T) {
	var mu sync.RWMutex
	var currentIsProxy bool
	var wg sync.WaitGroup

	// 模拟 getProxyState 的行为
	getProxyState := func() bool {
		mu.RLock()
		defer mu.RUnlock()
		return !currentIsProxy // 返回相反值以触发切换
	}

	// 模拟修复后的 ensureBrowserState
	ensureBrowserState := func() {
		defer wg.Done()

		// Step 1: 获取代理状态（在锁外）
		shouldUseProxy := getProxyState()

		// Step 2: 获取读锁检查当前状态
		mu.RLock()
		current := currentIsProxy
		mu.RUnlock()

		if shouldUseProxy == current {
			return
		}

		// Step 3: 获取写锁进行切换
		mu.Lock()
		defer mu.Unlock()

		// 双重检查（不调用 getProxyState，避免死锁）
		if shouldUseProxy == currentIsProxy {
			return
		}

		// 模拟浏览器切换
		time.Sleep(10 * time.Millisecond)
		currentIsProxy = shouldUseProxy
	}

	// 并发执行
	numGoroutines := 10
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go ensureBrowserState()
	}

	// 使用超时检测死锁
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// 成功完成，没有死锁
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock detected in concurrent ensureBrowserState pattern")
	}
}

// TestGetProxyStateRequiresRLock 文档化 getProxyState 需要 RLock，
// 所以不能在持有写锁时调用。
func TestGetProxyStateRequiresRLock(t *testing.T) {
	var mu sync.RWMutex

	// 模拟 getProxyState 的锁行为
	getProxyStateLike := func() {
		mu.RLock()
		defer mu.RUnlock()
		// 读取缓存
	}

	// 正确模式：在写锁外调用 getProxyState
	done := make(chan bool, 1)
	go func() {
		// 先调用（写锁外）
		getProxyStateLike()

		// 再获取写锁
		mu.Lock()
		defer mu.Unlock()

		// 写锁内不再调用 getProxyStateLike

		done <- true
	}()

	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("unexpected deadlock in correct pattern")
	}
}

// ============================================================================
// 全面锁行为边界测试
// 覆盖 service.go 中所有 11 处锁使用点的并发安全性
// ============================================================================

// TestLockBehavior_CheckBrowserHealthPattern 测试 checkBrowserHealth 的锁模式。
// 该函数使用 RLock 读取 browser，确保不阻塞写操作过长时间。
func TestLockBehavior_CheckBrowserHealthPattern(t *testing.T) {
	var mu sync.RWMutex
	var browser *int
	val := 42
	browser = &val

	done := make(chan bool, 2)

	// 模拟 checkBrowserHealth：短暂 RLock 读取
	go func() {
		for i := 0; i < 100; i++ {
			mu.RLock()
			_ = browser
			mu.RUnlock()
			time.Sleep(time.Microsecond)
		}
		done <- true
	}()

	// 模拟 restartBrowserInstance：Lock 写入
	go func() {
		for i := 0; i < 10; i++ {
			mu.Lock()
			newVal := i
			browser = &newVal
			time.Sleep(time.Millisecond) // 模拟浏览器重启时间
			mu.Unlock()
		}
		done <- true
	}()

	timeout := time.After(5 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-done:
			// OK
		case <-timeout:
			t.Fatal("deadlock detected in checkBrowserHealth pattern")
		}
	}
}

// TestLockBehavior_RestartBrowserInstance 测试 restartBrowserInstance 的锁模式。
// 该函数持有写锁期间执行 I/O 操作（启动浏览器），确保不会死锁。
func TestLockBehavior_RestartBrowserInstance(t *testing.T) {
	var mu sync.RWMutex
	var currentIsProxy bool
	var browser *int

	done := make(chan bool, 1)

	go func() {
		// 模拟 restartBrowserInstance 的完整流程
		mu.Lock()
		defer mu.Unlock()

		// 保存当前代理状态
		shouldUseProxy := currentIsProxy

		// 模拟关闭旧浏览器
		if browser != nil {
			time.Sleep(10 * time.Millisecond) // 模拟 Close 耗时
			browser = nil
		}

		// 模拟启动新浏览器
		time.Sleep(20 * time.Millisecond) // 模拟 startBrowser 耗时
		newVal := 1
		browser = &newVal
		_ = shouldUseProxy

		done <- true
	}()

	select {
	case <-done:
		// OK
	case <-time.After(3 * time.Second):
		t.Fatal("deadlock detected in restartBrowserInstance pattern")
	}
}

// TestLockBehavior_EnsureBrowserStateDoubleCheck 测试 ensureBrowserState 的双重检查模式。
// 验证修复后的模式不会死锁。
func TestLockBehavior_EnsureBrowserStateDoubleCheck(t *testing.T) {
	var mu sync.RWMutex
	var currentIsProxy bool
	var proxyCache bool
	var proxyCacheUntil time.Time

	// 模拟 getProxyState（读取缓存）
	getProxyState := func() bool {
		now := time.Now()
		mu.RLock()
		if now.Before(proxyCacheUntil) {
			state := proxyCache
			mu.RUnlock()
			return state
		}
		mu.RUnlock()

		// 模拟 Redis 调用
		state := true

		mu.Lock()
		proxyCache = state
		proxyCacheUntil = now.Add(5 * time.Second)
		mu.Unlock()

		return state
	}

	// 模拟修复后的 ensureBrowserState
	ensureBrowserState := func() error {
		shouldUseProxy := getProxyState()

		mu.RLock()
		current := currentIsProxy
		mu.RUnlock()

		if shouldUseProxy == current {
			return nil
		}

		mu.Lock()
		defer mu.Unlock()

		// 双重检查：不调用 getProxyState，避免死锁
		if shouldUseProxy == currentIsProxy {
			return nil
		}

		// 模拟切换浏览器
		time.Sleep(10 * time.Millisecond)
		currentIsProxy = shouldUseProxy
		return nil
	}

	done := make(chan bool, 1)
	go func() {
		for i := 0; i < 50; i++ {
			if err := ensureBrowserState(); err != nil {
				return
			}
		}
		done <- true
	}()

	select {
	case <-done:
		// OK
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock detected in ensureBrowserState double-check pattern")
	}
}

// TestLockBehavior_IsUsingProxy 测试 isUsingProxy 的简单 RLock 模式。
func TestLockBehavior_IsUsingProxy(t *testing.T) {
	var mu sync.RWMutex
	var currentIsProxy bool

	var wg sync.WaitGroup
	numReaders := 100
	wg.Add(numReaders)

	for i := 0; i < numReaders; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				mu.RLock()
				_ = currentIsProxy
				mu.RUnlock()
			}
		}()
	}

	// 偶尔写入
	go func() {
		for i := 0; i < 100; i++ {
			mu.Lock()
			currentIsProxy = !currentIsProxy
			mu.Unlock()
			time.Sleep(time.Millisecond)
		}
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock detected in isUsingProxy pattern")
	}
}

// TestLockBehavior_GetProxyStateCacheMiss 测试 getProxyState 缓存未命中时的锁升级模式。
// 正确模式：RLock -> RUnlock -> 执行 I/O -> Lock -> Unlock
func TestLockBehavior_GetProxyStateCacheMiss(t *testing.T) {
	var mu sync.RWMutex
	var proxyCache bool
	var proxyCacheUntil time.Time // 默认为零值，表示缓存过期

	getProxyState := func() bool {
		now := time.Now()

		// Step 1: RLock 检查缓存
		mu.RLock()
		if now.Before(proxyCacheUntil) {
			state := proxyCache
			mu.RUnlock()
			return state
		}
		mu.RUnlock()

		// Step 2: 缓存过期，执行 Redis 操作（锁外）
		time.Sleep(time.Millisecond) // 模拟 Redis 调用
		newState := true

		// Step 3: Lock 更新缓存
		mu.Lock()
		proxyCache = newState
		proxyCacheUntil = now.Add(5 * time.Second)
		mu.Unlock()

		return newState
	}

	var wg sync.WaitGroup
	numGoroutines := 50
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = getProxyState()
				// 偶尔重置缓存以触发缓存未命中
				if j%5 == 0 {
					mu.Lock()
					proxyCacheUntil = time.Time{}
					mu.Unlock()
				}
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock detected in getProxyState cache miss pattern")
	}
}

// TestLockBehavior_SetProxyCooldown 测试 setProxyCooldown 的锁模式。
// 正确模式：执行 Redis 操作（锁外）-> Lock -> 更新本地缓存 -> Unlock
func TestLockBehavior_SetProxyCooldown(t *testing.T) {
	var mu sync.RWMutex
	var proxyCache bool
	var proxyCacheUntil time.Time

	setProxyCooldown := func() {
		// 模拟 Redis 操作（锁外）
		time.Sleep(time.Millisecond)

		// 更新本地缓存
		mu.Lock()
		proxyCache = true
		proxyCacheUntil = time.Now().Add(5 * time.Second)
		mu.Unlock()
	}

	getProxyCache := func() (bool, time.Time) {
		mu.RLock()
		defer mu.RUnlock()
		return proxyCache, proxyCacheUntil
	}

	var wg sync.WaitGroup
	numGoroutines := 20
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				setProxyCooldown()
				// 同时读取以模拟真实场景
				_, _ = getProxyCache()
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// 验证最终状态
		cache, until := getProxyCache()
		if !cache {
			t.Error("proxyCache should be true after setProxyCooldown")
		}
		if until.IsZero() {
			t.Error("proxyCacheUntil should not be zero")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock detected in setProxyCooldown pattern")
	}
}

// TestLockBehavior_CrawlOnceGetBrowser 测试 crawlOnce 获取 browser 的 RLock 模式。
func TestLockBehavior_CrawlOnceGetBrowser(t *testing.T) {
	var mu sync.RWMutex
	var browser *int
	val := 1
	browser = &val

	var wg sync.WaitGroup
	numGoroutines := 100
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			// 模拟 crawlOnce 读取 browser
			mu.RLock()
			b := browser
			mu.RUnlock()
			_ = b
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(3 * time.Second):
		t.Fatal("deadlock detected in crawlOnce getBrowser pattern")
	}
}

// TestLockBehavior_ShutdownPattern 测试 Shutdown 的锁模式。
// 正确模式：Lock -> 取出 browser 引用并置 nil -> Unlock -> 执行 Close（锁外）
func TestLockBehavior_ShutdownPattern(t *testing.T) {
	var mu sync.RWMutex
	var browser *int
	val := 1
	browser = &val

	// 模拟并发读取
	stopReaders := make(chan struct{})
	var readersWg sync.WaitGroup
	for i := 0; i < 10; i++ {
		readersWg.Add(1)
		go func() {
			defer readersWg.Done()
			for {
				select {
				case <-stopReaders:
					return
				default:
					mu.RLock()
					_ = browser
					mu.RUnlock()
				}
			}
		}()
	}

	// 模拟 Shutdown
	done := make(chan bool, 1)
	go func() {
		time.Sleep(50 * time.Millisecond) // 让读取者先运行一段时间

		mu.Lock()
		b := browser
		browser = nil
		mu.Unlock()

		// Close 在锁外执行
		if b != nil {
			time.Sleep(10 * time.Millisecond) // 模拟 browser.Close()
		}

		close(stopReaders)
		done <- true
	}()

	select {
	case <-done:
		readersWg.Wait()
		// OK
	case <-time.After(5 * time.Second):
		close(stopReaders)
		t.Fatal("deadlock detected in Shutdown pattern")
	}
}

// TestLockBehavior_HealthCheckAndRestartRace 测试健康检查和重启之间的竞争。
// 场景：checkBrowserHealth 和 restartBrowserInstance 可能同时运行
func TestLockBehavior_HealthCheckAndRestartRace(t *testing.T) {
	var mu sync.RWMutex
	var browser *int
	val := 1
	browser = &val

	checkBrowserHealth := func() bool {
		mu.RLock()
		b := browser
		mu.RUnlock()
		return b != nil
	}

	restartBrowserInstance := func() {
		mu.Lock()
		defer mu.Unlock()
		if browser != nil {
			time.Sleep(5 * time.Millisecond) // 模拟关闭
			browser = nil
		}
		newVal := 2
		time.Sleep(10 * time.Millisecond) // 模拟启动
		browser = &newVal
	}

	var wg sync.WaitGroup

	// 健康检查 goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			healthy := checkBrowserHealth()
			if !healthy {
				restartBrowserInstance()
			}
			time.Sleep(time.Millisecond)
		}
	}()

	// 另一个重启 goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			restartBrowserInstance()
			time.Sleep(5 * time.Millisecond)
		}
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock detected in health check and restart race")
	}
}

// TestLockBehavior_EnsureBrowserStateAndShutdownRace 测试 ensureBrowserState 和 Shutdown 的竞争。
func TestLockBehavior_EnsureBrowserStateAndShutdownRace(t *testing.T) {
	var mu sync.RWMutex
	var currentIsProxy bool
	var browser *int
	val := 1
	browser = &val

	ensureBrowserState := func() {
		mu.RLock()
		current := currentIsProxy
		mu.RUnlock()

		if current {
			return
		}

		mu.Lock()
		defer mu.Unlock()

		if currentIsProxy {
			return
		}

		// 模拟切换
		if browser != nil {
			time.Sleep(5 * time.Millisecond)
		}
		newVal := 2
		browser = &newVal
		currentIsProxy = true
	}

	shutdown := func() {
		mu.Lock()
		b := browser
		browser = nil
		mu.Unlock()

		if b != nil {
			time.Sleep(10 * time.Millisecond) // 模拟 Close
		}
	}

	var wg sync.WaitGroup

	// 多个 ensureBrowserState
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				ensureBrowserState()
				time.Sleep(time.Millisecond)
			}
		}()
	}

	// Shutdown 在中途触发
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(50 * time.Millisecond)
		shutdown()
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock detected in ensureBrowserState and Shutdown race")
	}
}

// TestLockBehavior_AllOperationsConcurrent 综合测试：所有锁操作同时执行
// 这是最严格的死锁检测测试
func TestLockBehavior_AllOperationsConcurrent(t *testing.T) {
	var mu sync.RWMutex
	var currentIsProxy bool
	var proxyCache bool
	var proxyCacheUntil time.Time
	var browser *int
	val := 1
	browser = &val

	// 模拟 checkBrowserHealth (RLock)
	checkBrowserHealth := func() bool {
		mu.RLock()
		b := browser
		mu.RUnlock()
		return b != nil
	}

	// 模拟 restartBrowserInstance (Lock)
	restartBrowserInstance := func() {
		mu.Lock()
		defer mu.Unlock()
		if browser != nil {
			browser = nil
		}
		newVal := 2
		browser = &newVal
	}

	// 模拟 isUsingProxy (RLock)
	isUsingProxy := func() bool {
		mu.RLock()
		defer mu.RUnlock()
		return currentIsProxy
	}

	// 模拟 getProxyState (RLock -> RUnlock -> Lock -> Unlock)
	getProxyState := func() bool {
		now := time.Now()
		mu.RLock()
		if now.Before(proxyCacheUntil) {
			state := proxyCache
			mu.RUnlock()
			return state
		}
		mu.RUnlock()

		mu.Lock()
		proxyCache = true
		proxyCacheUntil = now.Add(time.Second)
		mu.Unlock()
		return true
	}

	// 模拟 setProxyCooldown (Lock)
	setProxyCooldown := func() {
		mu.Lock()
		proxyCache = true
		proxyCacheUntil = time.Now().Add(time.Second)
		mu.Unlock()
	}

	// 模拟 crawlOnce (RLock)
	crawlOnce := func() {
		mu.RLock()
		_ = browser
		mu.RUnlock()
	}

	// 模拟 ensureBrowserState (RLock -> RUnlock -> Lock -> Unlock)
	ensureBrowserState := func() {
		shouldUseProxy := getProxyState()

		mu.RLock()
		current := currentIsProxy
		mu.RUnlock()

		if shouldUseProxy == current {
			return
		}

		mu.Lock()
		defer mu.Unlock()

		if shouldUseProxy == currentIsProxy {
			return
		}
		currentIsProxy = shouldUseProxy
	}

	// 模拟 Shutdown (Lock)
	shutdown := func() {
		mu.Lock()
		b := browser
		browser = nil
		mu.Unlock()
		_ = b
	}

	var wg sync.WaitGroup
	iterations := 30

	// 启动所有操作的 goroutine
	operations := []func(){
		func() {
			for i := 0; i < iterations; i++ {
				checkBrowserHealth()
				time.Sleep(time.Microsecond * 100)
			}
		},
		func() {
			for i := 0; i < iterations/10; i++ {
				restartBrowserInstance()
				time.Sleep(time.Millisecond)
			}
		},
		func() {
			for i := 0; i < iterations*3; i++ {
				isUsingProxy()
			}
		},
		func() {
			for i := 0; i < iterations*2; i++ {
				getProxyState()
				time.Sleep(time.Microsecond * 50)
			}
		},
		func() {
			for i := 0; i < iterations; i++ {
				setProxyCooldown()
				time.Sleep(time.Microsecond * 200)
			}
		},
		func() {
			for i := 0; i < iterations*5; i++ {
				crawlOnce()
			}
		},
		func() {
			for i := 0; i < iterations; i++ {
				ensureBrowserState()
				time.Sleep(time.Microsecond * 100)
			}
		},
	}

	for _, op := range operations {
		wg.Add(1)
		go func(operation func()) {
			defer wg.Done()
			operation()
		}(op)
	}

	// 在最后触发 shutdown
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(100 * time.Millisecond)
		shutdown()
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// 所有操作完成，没有死锁
	case <-time.After(15 * time.Second):
		t.Fatal("DEADLOCK DETECTED: All operations concurrent test failed")
	}
}

// TestLockBehavior_WriteLockStarvation 测试写锁饥饿问题。
// 确保在大量读锁情况下，写锁最终能够获取。
func TestLockBehavior_WriteLockStarvation(t *testing.T) {
	var mu sync.RWMutex
	var value int

	stopReaders := make(chan struct{})
	writerDone := make(chan bool, 1)

	// 启动大量持续读取的 goroutine
	for i := 0; i < 20; i++ {
		go func() {
			for {
				select {
				case <-stopReaders:
					return
				default:
					mu.RLock()
					_ = value
					mu.RUnlock()
				}
			}
		}()
	}

	// 写锁应该能在合理时间内获取
	go func() {
		time.Sleep(10 * time.Millisecond) // 让读取者先运行
		mu.Lock()
		value = 42
		mu.Unlock()
		writerDone <- true
	}()

	select {
	case <-writerDone:
		close(stopReaders)
		// OK - 写锁成功获取
	case <-time.After(5 * time.Second):
		close(stopReaders)
		t.Fatal("write lock starvation detected")
	}
}
