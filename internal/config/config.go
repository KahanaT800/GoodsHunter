package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/spf13/viper"
)

// Config 保存应用程序配置。
type Config struct {
	App      AppConfig      `json:"app"`
	MySQL    MySQLConfig    `json:"mysql"`
	Redis    RedisConfig    `json:"redis"`
	Browser  BrowserConfig  `json:"browser"`
	Email    EmailConfig    `json:"email"`
	Security SecurityConfig `json:"security"`
}

// AppConfig 应用程序基础配置。
type AppConfig struct {
	Env              string        `json:"env"`                // 运行环境: local / prod
	LogLevel         string        `json:"log_level"`          // 日志级别: debug / info / warn / error
	HTTPAddr         string        `json:"http_addr"`          // API 服务监听地址
	CrawlerGRPCAddr  string        `json:"crawler_grpc_addr"`  // 爬虫 gRPC 地址
	ScheduleInterval time.Duration `json:"schedule_interval"`  // 调度间隔（如 "5m"）
	NewItemDuration  time.Duration `json:"new_item_duration"`  // 新商品热度持续时间（如 "10m"）
	GuestIdleTimeout time.Duration `json:"guest_idle_timeout"` // Guest 无操作超时（如 "10m"）
	GuestHeartbeat   time.Duration `json:"guest_heartbeat"`    // Guest 心跳间隔（如 "5m"）
	MaxTasksPerUser  int           `json:"max_tasks_per_user"` // 每个用户最大任务数
	MaxItemsPerTask  int           `json:"max_items_per_task"` // 每个任务保留的最大商品数
	WorkerPoolSize   int           `json:"worker_pool_size"`   // Worker Pool 大小（并发任务数）
	QueueCapacity    int           `json:"queue_capacity"`     // 队列容量
	JWTSecret        string        `json:"jwt_secret"`         // JWT 签名密钥
	RateLimit        float64       `json:"rate_limit"`         // 限流速率（token/s）
	RateBurst        float64       `json:"rate_burst"`         // 限流桶容量
	QueueBatchSize   int           `json:"queue_batch_size"`   // 轮询批量入队大小
	DedupWindow      int           `json:"dedup_window"`       // URL 去重窗口（秒）
	ProxyCooldown    time.Duration `json:"proxy_cooldown"`     // 直连失败后代理冷却时间
	MaxTasks         int           `json:"max_tasks"`          // 重启前最大任务数

	// Redis Streams 任务队列配置
	EnableRedisQueue bool   `json:"enable_redis_queue"` // 是否启用 Redis Streams 队列（开关）
	TaskQueueStream  string `json:"task_queue_stream"`  // Redis Stream 名称
	TaskQueueGroup   string `json:"task_queue_group"`   // Consumer Group 名称
}

// MySQLConfig MySQL 数据库配置。
type MySQLConfig struct {
	DSN string `json:"dsn"` // 数据库连接字符串
}

// RedisConfig Redis 缓存配置。
type RedisConfig struct {
	Addr     string `json:"addr"`     // Redis 地址 (host:port)
	Password string `json:"password"` // Redis 密码
}

// BrowserConfig 爬虫浏览器配置。
type BrowserConfig struct {
	BinPath        string `json:"bin_path"`        // 浏览器可执行文件路径
	ProxyURL       string `json:"proxy_url"`       // 代理服务器 URL
	Headless       bool   `json:"headless"`        // 是否使用无头模式
	MaxConcurrency int    `json:"max_concurrency"` // 最大并发页面数
	MaxFetchCount  int    `json:"max_fetch_count"` // 每次爬取最大数量
}

// EmailConfig 邮件通知配置。
type EmailConfig struct {
	SMTPHost  string `json:"smtp_host"`
	SMTPPort  int    `json:"smtp_port"`
	SMTPUser  string `json:"smtp_user"`
	SMTPPass  string `json:"smtp_pass"`
	FromEmail string `json:"from_email"`
}

// SecurityConfig 安全相关配置。
type SecurityConfig struct {
	JWTSecret  string `json:"jwt_secret"`  // JWT 签名密钥
	InviteCode string `json:"invite_code"` // 邀请码（为空表示禁止注册）
}

// Load 从 JSON 文件加载配置。
//
// 它会尝试读取 configs/config.json 文件，如果不存在则使用默认值。
//
// 参数:
//
//	configPath: 配置文件路径（如果为空则使用默认路径 "configs/config.json")
//
// 返回值:
//
//	*Config: 加载完成的配置对象
//	error: 加载失败返回错误
func Load(configPath ...string) (*Config, error) {
	path := "configs/config.json"
	if len(configPath) > 0 && configPath[0] != "" {
		path = configPath[0]
	}

	// 如果配置文件不存在，使用默认配置
	if _, err := os.Stat(path); os.IsNotExist(err) {
		cfg := getDefaultConfig()
		// 即使没有配置文件，也允许环境变量覆盖默认值
		applyEnvOverrides(cfg)
		return cfg, nil
	}

	// 读取配置文件
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	// 解析 JSON
	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	// 应用默认值（对于未设置的字段）
	applyDefaults(cfg)

	// 环境变量优先覆盖配置
	applyEnvOverrides(cfg)

	return cfg, nil
}

// LoadOrDefault 加载配置，如果失败则返回默认配置（不报错）。
func LoadOrDefault(configPath ...string) *Config {
	cfg, err := Load(configPath...)
	if err != nil {
		fallback := getDefaultConfig()
		applyEnvOverrides(fallback)
		return fallback
	}
	return cfg
}

// Save 保存配置到 JSON 文件。
//
// 参数:
//
//	path: 保存路径
//	cfg: 配置对象
//
// 返回值:
//
//	error: 保存失败返回错误
func Save(path string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}

	return nil
}

// getDefaultConfig 返回默认配置。
func getDefaultConfig() *Config {
	return &Config{
		App: AppConfig{
			Env:              "local",
			LogLevel:         "info",
			HTTPAddr:         ":8081",
			CrawlerGRPCAddr:  "localhost:50051",
			ScheduleInterval: 5 * time.Minute,
			NewItemDuration:  1 * time.Hour,
			GuestIdleTimeout: 10 * time.Minute,
			GuestHeartbeat:   5 * time.Minute,
			MaxTasksPerUser:  3,
			MaxItemsPerTask:  300,
			WorkerPoolSize:   50,
			QueueCapacity:    1000,
			JWTSecret:        "dev_secret_change_me",
			RateLimit:        3,
			RateBurst:        5,
			QueueBatchSize:   100,
			DedupWindow:      3600,
			ProxyCooldown:    10 * time.Minute,
			MaxTasks:         500,

			// Redis Streams 默认配置
			EnableRedisQueue: false, // 默认关闭，渐进式升级
			TaskQueueStream:  "goodshunter:task:queue",
			TaskQueueGroup:   "scheduler_group",
		},
		MySQL: MySQLConfig{
			DSN: "root:password@tcp(localhost:3306)/goodshunter?parseTime=true&loc=Local",
		},
		Redis: RedisConfig{
			Addr:     "localhost:6379",
			Password: "",
		},
		Browser: BrowserConfig{
			BinPath:        "",
			ProxyURL:       "",
			Headless:       true,
			MaxConcurrency: 5,
			MaxFetchCount:  50,
		},
		Email: EmailConfig{
			SMTPHost:  "smtp.gmail.com",
			SMTPPort:  587,
			SMTPUser:  "",
			SMTPPass:  "",
			FromEmail: "",
		},
		Security: SecurityConfig{
			JWTSecret:  "dev_secret_change_me",
			InviteCode: "",
		},
	}
}

// applyDefaults 对未设置的字段应用默认值。
func applyDefaults(cfg *Config) {
	defaults := getDefaultConfig()

	if cfg.App.Env == "" {
		cfg.App.Env = defaults.App.Env
	}
	if cfg.App.LogLevel == "" {
		cfg.App.LogLevel = defaults.App.LogLevel
	}
	if cfg.App.HTTPAddr == "" {
		cfg.App.HTTPAddr = defaults.App.HTTPAddr
	}
	if cfg.App.CrawlerGRPCAddr == "" {
		cfg.App.CrawlerGRPCAddr = defaults.App.CrawlerGRPCAddr
	}
	if cfg.App.ScheduleInterval == 0 {
		cfg.App.ScheduleInterval = defaults.App.ScheduleInterval
	}
	if cfg.App.NewItemDuration == 0 {
		cfg.App.NewItemDuration = defaults.App.NewItemDuration
	}
	if cfg.App.GuestIdleTimeout == 0 {
		cfg.App.GuestIdleTimeout = defaults.App.GuestIdleTimeout
	}
	if cfg.App.GuestHeartbeat == 0 {
		cfg.App.GuestHeartbeat = defaults.App.GuestHeartbeat
	}
	if cfg.App.MaxTasksPerUser == 0 {
		cfg.App.MaxTasksPerUser = defaults.App.MaxTasksPerUser
	}
	if cfg.App.MaxItemsPerTask == 0 {
		cfg.App.MaxItemsPerTask = 300 // 默认保留 300 个
	}
	if cfg.App.WorkerPoolSize == 0 {
		cfg.App.WorkerPoolSize = defaults.App.WorkerPoolSize
	}
	if cfg.App.QueueCapacity == 0 {
		cfg.App.QueueCapacity = defaults.App.QueueCapacity
	}
	if cfg.App.RateLimit == 0 {
		cfg.App.RateLimit = defaults.App.RateLimit
	}
	if cfg.App.RateBurst == 0 {
		cfg.App.RateBurst = defaults.App.RateBurst
	}
	if cfg.App.QueueBatchSize == 0 {
		cfg.App.QueueBatchSize = defaults.App.QueueBatchSize
	}
	if cfg.App.DedupWindow == 0 {
		cfg.App.DedupWindow = defaults.App.DedupWindow
	}
	if cfg.App.ProxyCooldown == 0 {
		cfg.App.ProxyCooldown = defaults.App.ProxyCooldown
	}
	if cfg.App.MaxTasks == 0 {
		cfg.App.MaxTasks = defaults.App.MaxTasks
	}
	// Redis Streams 默认值
	if cfg.App.TaskQueueStream == "" {
		cfg.App.TaskQueueStream = defaults.App.TaskQueueStream
	}
	if cfg.App.TaskQueueGroup == "" {
		cfg.App.TaskQueueGroup = defaults.App.TaskQueueGroup
	}
	if cfg.Security.JWTSecret == "" {
		if cfg.App.JWTSecret != "" {
			cfg.Security.JWTSecret = cfg.App.JWTSecret
		} else {
			cfg.Security.JWTSecret = defaults.Security.JWTSecret
		}
	}
	if cfg.Security.InviteCode == "" {
		cfg.Security.InviteCode = defaults.Security.InviteCode
	}
	if cfg.App.JWTSecret == "" {
		cfg.App.JWTSecret = cfg.Security.JWTSecret
	}
	if cfg.Browser.MaxConcurrency == 0 {
		cfg.Browser.MaxConcurrency = defaults.Browser.MaxConcurrency
	}
	if cfg.Browser.MaxFetchCount == 0 {
		cfg.Browser.MaxFetchCount = defaults.Browser.MaxFetchCount
	}
	if cfg.Email.SMTPPort == 0 {
		cfg.Email.SMTPPort = defaults.Email.SMTPPort
	}
}

func applyEnvOverrides(cfg *Config) {
	viper.AutomaticEnv()

	_ = viper.BindEnv("db_host", "DB_HOST")
	_ = viper.BindEnv("db_password", "DB_PASSWORD")
	_ = viper.BindEnv("redis_addr", "REDIS_ADDR")
	_ = viper.BindEnv("redis_password", "REDIS_PASSWORD")
	_ = viper.BindEnv("smtp_pass", "SMTP_PASS")
	_ = viper.BindEnv("jwt_secret", "JWT_SECRET")
	_ = viper.BindEnv("invite_code", "INVITE_CODE")
	_ = viper.BindEnv("chrome_bin", "CHROME_BIN")

	if v := os.Getenv("APP_ENV"); v != "" {
		cfg.App.Env = v
	}
	if v := os.Getenv("APP_LOG_LEVEL"); v != "" {
		cfg.App.LogLevel = v
	}
	if v := os.Getenv("APP_HTTP_ADDR"); v != "" {
		cfg.App.HTTPAddr = v
	}
	if v := os.Getenv("APP_CRAWLER_GRPC_ADDR"); v != "" {
		cfg.App.CrawlerGRPCAddr = v
	}
	if v := os.Getenv("APP_SCHEDULE_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.App.ScheduleInterval = d
		}
	}
	if v := os.Getenv("APP_NEW_ITEM_DURATION"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.App.NewItemDuration = d
		}
	}
	if v := os.Getenv("APP_GUEST_IDLE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.App.GuestIdleTimeout = d
		}
	}
	if v := os.Getenv("APP_GUEST_HEARTBEAT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.App.GuestHeartbeat = d
		}
	}
	if v := os.Getenv("APP_MAX_TASKS_PER_USER"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.App.MaxTasksPerUser = i
		}
	}
	if v := os.Getenv("APP_MAX_ITEMS_PER_TASK"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.App.MaxItemsPerTask = i
		}
	}
	if v := os.Getenv("APP_WORKER_POOL_SIZE"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.App.WorkerPoolSize = i
		}
	}
	if v := os.Getenv("APP_QUEUE_CAPACITY"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.App.QueueCapacity = i
		}
	}
	if v := os.Getenv("APP_RATE_LIMIT"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.App.RateLimit = f
		}
	}
	if v := os.Getenv("APP_RATE_BURST"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.App.RateBurst = f
		}
	}
	if v := os.Getenv("APP_QUEUE_BATCH_SIZE"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.App.QueueBatchSize = i
		}
	}
	if v := os.Getenv("APP_DEDUP_WINDOW"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.App.DedupWindow = i
		}
	}
	if v := os.Getenv("PROXY_COOLDOWN"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.App.ProxyCooldown = d
		}
	}
	if v := os.Getenv("MAX_TASKS"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.App.MaxTasks = i
		}
	}

	// Redis Streams 环境变量
	if v := os.Getenv("APP_ENABLE_REDIS_QUEUE"); v != "" {
		cfg.App.EnableRedisQueue = v == "true" || v == "1"
	}
	if v := os.Getenv("APP_TASK_QUEUE_STREAM"); v != "" {
		cfg.App.TaskQueueStream = v
	}
	if v := os.Getenv("APP_TASK_QUEUE_GROUP"); v != "" {
		cfg.App.TaskQueueGroup = v
	}

	if v := viper.GetString("jwt_secret"); v != "" {
		cfg.Security.JWTSecret = v
		cfg.App.JWTSecret = v
	}
	if v := os.Getenv("APP_INVITE_CODE"); v != "" {
		cfg.Security.InviteCode = v
	}
	if v := viper.GetString("invite_code"); v != "" {
		cfg.Security.InviteCode = v
	}

	if v := os.Getenv("DB_DSN"); v != "" {
		cfg.MySQL.DSN = v
	} else if hasAnyEnv("DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME") || viper.GetString("db_host") != "" || viper.GetString("db_password") != "" {
		parsed := parseMySQLDSN(cfg.MySQL.DSN)
		if v := viper.GetString("db_host"); v != "" {
			host := v
			port := getenvDefault("DB_PORT", parsed.Addr, "3306")
			parsed.Addr = host + ":" + port
		} else if v := os.Getenv("DB_PORT"); v != "" {
			host := parsed.Addr
			if strings.Contains(host, ":") {
				host = strings.Split(host, ":")[0]
			}
			parsed.Addr = host + ":" + v
		}
		if v := os.Getenv("DB_USER"); v != "" {
			parsed.User = v
		}
		if v := viper.GetString("db_password"); v != "" {
			parsed.Passwd = v
		}
		if v := os.Getenv("DB_NAME"); v != "" {
			parsed.DBName = v
		}
		cfg.MySQL.DSN = parsed.FormatDSN()
	}

	if v := viper.GetString("redis_addr"); v != "" {
		cfg.Redis.Addr = v
	}
	if v := viper.GetString("redis_password"); v != "" {
		cfg.Redis.Password = v
	}

	if v := viper.GetString("chrome_bin"); v != "" {
		cfg.Browser.BinPath = v
	}
	if v := os.Getenv("HTTP_PROXY"); v != "" {
		cfg.Browser.ProxyURL = v
	} else if v := os.Getenv("BROWSER_PROXY_URL"); v != "" {
		cfg.Browser.ProxyURL = v
	}
	if v := os.Getenv("BROWSER_HEADLESS"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Browser.Headless = b
		}
	}
	if v := os.Getenv("BROWSER_MAX_CONCURRENCY"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.Browser.MaxConcurrency = i
		}
	}
	if v := os.Getenv("BROWSER_MAX_FETCH_COUNT"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.Browser.MaxFetchCount = i
		}
	}

	if v := os.Getenv("SMTP_HOST"); v != "" {
		cfg.Email.SMTPHost = v
	}
	if v := os.Getenv("SMTP_PORT"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.Email.SMTPPort = i
		}
	}
	if v := os.Getenv("SMTP_USER"); v != "" {
		cfg.Email.SMTPUser = v
	}
	if v := viper.GetString("smtp_pass"); v != "" {
		cfg.Email.SMTPPass = v
	}
	if v := os.Getenv("SMTP_FROM"); v != "" {
		cfg.Email.FromEmail = v
	}
}

func hasAnyEnv(keys ...string) bool {
	for _, key := range keys {
		if os.Getenv(key) != "" {
			return true
		}
	}
	return false
}

func getenvDefault(envKey, fallbackAddr, defaultValue string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	if fallbackAddr == "" {
		return defaultValue
	}
	if strings.Contains(fallbackAddr, ":") {
		parts := strings.Split(fallbackAddr, ":")
		if len(parts) == 2 && parts[1] != "" {
			return parts[1]
		}
	}
	return defaultValue
}

func parseMySQLDSN(dsn string) *mysql.Config {
	if dsn == "" {
		return &mysql.Config{
			User:   "root",
			Passwd: "",
			Net:    "tcp",
			Addr:   "localhost:3306",
			DBName: "goodshunter",
			Params: map[string]string{
				"parseTime": "true",
				"loc":       "Local",
			},
		}
	}
	parsed, err := mysql.ParseDSN(dsn)
	if err != nil {
		return &mysql.Config{
			User:   "root",
			Passwd: "",
			Net:    "tcp",
			Addr:   "localhost:3306",
			DBName: "goodshunter",
			Params: map[string]string{
				"parseTime": "true",
				"loc":       "Local",
			},
		}
	}
	return parsed
}

// UnmarshalJSON 自定义 JSON 解析，支持时间Duration字符串。
func (a *AppConfig) UnmarshalJSON(data []byte) error {
	type Alias AppConfig
	aux := &struct {
		ScheduleInterval string `json:"schedule_interval"`
		NewItemDuration  string `json:"new_item_duration"`
		GuestIdleTimeout string `json:"guest_idle_timeout"`
		GuestHeartbeat   string `json:"guest_heartbeat"`
		*Alias
	}{
		Alias: (*Alias)(a),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	if aux.ScheduleInterval != "" {
		duration, err := time.ParseDuration(aux.ScheduleInterval)
		if err != nil {
			return fmt.Errorf("invalid schedule_interval format: %w", err)
		}
		a.ScheduleInterval = duration
	}

	if aux.NewItemDuration != "" {
		duration, err := time.ParseDuration(aux.NewItemDuration)
		if err != nil {
			return fmt.Errorf("invalid new_item_duration format: %w", err)
		}
		a.NewItemDuration = duration
	}
	if aux.GuestIdleTimeout != "" {
		duration, err := time.ParseDuration(aux.GuestIdleTimeout)
		if err != nil {
			return fmt.Errorf("invalid guest_idle_timeout format: %w", err)
		}
		a.GuestIdleTimeout = duration
	}
	if aux.GuestHeartbeat != "" {
		duration, err := time.ParseDuration(aux.GuestHeartbeat)
		if err != nil {
			return fmt.Errorf("invalid guest_heartbeat format: %w", err)
		}
		a.GuestHeartbeat = duration
	}

	return nil
}

// MarshalJSON 自定义 JSON 序列化，将 Duration 转为字符串。
func (a AppConfig) MarshalJSON() ([]byte, error) {
	type Alias AppConfig
	return json.Marshal(&struct {
		ScheduleInterval string `json:"schedule_interval"`
		NewItemDuration  string `json:"new_item_duration"`
		GuestIdleTimeout string `json:"guest_idle_timeout"`
		GuestHeartbeat   string `json:"guest_heartbeat"`
		*Alias
	}{
		ScheduleInterval: a.ScheduleInterval.String(),
		NewItemDuration:  a.NewItemDuration.String(),
		GuestIdleTimeout: a.GuestIdleTimeout.String(),
		GuestHeartbeat:   a.GuestHeartbeat.String(),
		Alias:            (*Alias)(&a),
	})
}
