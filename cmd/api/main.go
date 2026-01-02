package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"goodshunter/internal/api"
	"goodshunter/internal/config"
	"goodshunter/internal/pkg/logger"
	"goodshunter/internal/pkg/redisqueue"
)

// main 是 API 服务的入口函数。
//
// 它负责：
// 1. 加载配置
// 2. 初始化日志
// 3. 初始化并启动 API 服务器
func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	appLogger := logger.NewDefault(cfg.App.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	redisQueue := redisqueue.NewClient(cfg.Redis.Addr, cfg.Redis.Password)
	srv, err := api.NewServer(ctx, cfg, appLogger, redisQueue)
	if err != nil {
		appLogger.Error("init server failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
	if err := srv.SeedDemoData(ctx); err != nil {
		appLogger.Error("seed demo data failed", slog.String("error", err.Error()))
		os.Exit(1)
	}

	srv.StartScheduler(ctx)

	httpServer := &http.Server{
		Addr:    cfg.App.HTTPAddr,
		Handler: srv.Router(),
	}

	go func() {
		appLogger.Info("api server listening", slog.String("addr", cfg.App.HTTPAddr))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			appLogger.Error("server run failed", slog.String("error", err.Error()))
		}
	}()

	<-ctx.Done()
	appLogger.Info("shutting down api server...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		appLogger.Error("http shutdown failed", slog.String("error", err.Error()))
	}
	time.Sleep(5 * time.Second)
	if err := srv.Close(); err != nil {
		appLogger.Error("close resources failed", slog.String("error", err.Error()))
	}
}
