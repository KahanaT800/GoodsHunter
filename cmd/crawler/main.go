package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"goodshunter/internal/config"
	crawler "goodshunter/internal/crawler"
	"goodshunter/internal/pkg/logger"
	"goodshunter/internal/pkg/redisqueue"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// main 是爬虫服务的入口函数。
//
// 它负责：
// 1. 加载配置
// 2. 初始化日志记录器
// 3. 启动爬虫服务实例
// 4. 启动 Redis Worker 与 Metrics 服务
// 5. 优雅关闭
func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	appLogger := logger.NewDefault(cfg.App.LogLevel)
	ctx := context.Background()

	maxConcurrent := cfg.App.RateLimit * 30
	if cfg.App.RateLimit > 0 && float64(cfg.App.WorkerPoolSize) > maxConcurrent {
		appLogger.Warn("worker pool size is significantly higher than rate limit throughput capacity",
			slog.Int("worker_pool_size", cfg.App.WorkerPoolSize),
			slog.Float64("rate_limit", cfg.App.RateLimit),
			slog.Float64("throughput_capacity", maxConcurrent))
	}

	redisQueue := redisqueue.NewClient(cfg.Redis.Addr, cfg.Redis.Password)
	service, err := crawler.NewService(ctx, cfg, appLogger, redisQueue)
	if err != nil {
		appLogger.Error("init crawler service failed", slog.String("error", err.Error()))
		os.Exit(1)
	}

	workerCtx, stopWorkers := context.WithCancel(context.Background())
	go func() {
		// 添加保险丝
		defer func() {
			if r := recover(); r != nil {
				appLogger.Error("PANIC in redis worker loop", slog.Any("panic", r))
				// 注意：这里 Panic 后循环就结束了，Worker 实际上停止了。
				// 在生产环境中，你可能希望它能自动重启，或者让主进程退出触发 Docker 重启。
				// 记录日志后，通知主进程退出，让 Docker 负责重启，保持状态干净。
				os.Exit(1)
			}
		}()

		appLogger.Info("starting redis worker loop")
		if err := service.StartWorker(workerCtx); err != nil && err != context.Canceled {
			appLogger.Error("redis worker loop stopped", slog.String("error", err.Error()))
		}
	}()

	metricsAddr := ":2112"
	if v := os.Getenv("CRAWLER_METRICS_ADDR"); v != "" {
		metricsAddr = v
	}
	metricsServer := &http.Server{
		Addr:    metricsAddr,
		Handler: promhttp.Handler(),
	}
	go func() {
		appLogger.Info("crawler metrics server started", slog.String("addr", metricsAddr))
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			appLogger.Error("metrics server stopped with error", slog.String("error", err.Error()))
		}
	}()

	// 等待中断信号
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	restartCh := service.RestartSignal()
	select {
	case sig := <-sigCh:
		appLogger.Info("received os signal", slog.String("signal", sig.String()))
	case <-restartCh:
		appLogger.Info("restart requested by service (max tasks reached)")
	}

	appLogger.Info("shutting down crawler service...")

	// 优雅关闭
	// 1. 停止拉取新任务
	stopWorkers()

	// 2. 关闭 worker pool（等待所有任务完成）
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := metricsServer.Shutdown(shutdownCtx); err != nil {
		appLogger.Error("metrics shutdown error", slog.String("error", err.Error()))
	}

	if err := service.Shutdown(shutdownCtx); err != nil {
		appLogger.Error("worker pool shutdown error", slog.String("error", err.Error()))
	} else {
		appLogger.Info("worker pool shutdown completed")
	}

	appLogger.Info("crawler service stopped gracefully")
}
