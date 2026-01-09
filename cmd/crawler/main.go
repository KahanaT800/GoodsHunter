package main

import (
	"context"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"goodshunter/internal/config"
	crawler "goodshunter/internal/crawler"
	"goodshunter/internal/pkg/logger"
	"goodshunter/proto/pb"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
)

// main 是爬虫服务的入口函数。
//
// 它负责：
// 1. 加载配置
// 2. 初始化日志记录器
// 3. 启动爬虫服务实例
// 4. 启动 gRPC 服务器并监听端口
// 5. 优雅关闭
func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	appLogger := logger.NewDefault(cfg.App.LogLevel)
	ctx := context.Background()

	service, err := crawler.NewService(ctx, cfg, appLogger)
	if err != nil {
		appLogger.Error("init crawler service failed", slog.String("error", err.Error()))
		os.Exit(1)
	}

	addr := ":50051"
	if v := os.Getenv("CRAWLER_GRPC_ADDR"); v != "" {
		addr = v
	}

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		appLogger.Error("listen failed", slog.String("addr", addr), slog.String("error", err.Error()))
		os.Exit(1)
	}

	server := grpc.NewServer()
	pb.RegisterCrawlerServiceServer(server, service)

	// 启动 gRPC 服务器（非阻塞）
	go func() {
		appLogger.Info("crawler gRPC server started", slog.String("addr", addr))
		if err := server.Serve(lis); err != nil {
			appLogger.Error("server stopped with error", slog.String("error", err.Error()))
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
	<-sigCh

	appLogger.Info("shutting down crawler service...")

	// 优雅关闭
	// 1. 停止接收新请求
	server.GracefulStop()
	appLogger.Info("gRPC server stopped")

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
