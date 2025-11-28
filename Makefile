.PHONY: all up down logs ps build clean test lint vet run run-api run-crawler dev stop-dev help

# 默认目标：执行本地质量检查和编译
all: lint test build

# ==============================================================================
# Docker Environment (容器化操作)
# ==============================================================================

up:
	@echo "Generating SSL certs..."
	./scripts/gen_cert.sh
	@echo "Starting services..."
	docker compose --env-file .env up -d --build

down:
	@echo "Stopping services..."
	docker compose down

logs:
	docker compose logs -f --tail=200

ps:
	docker compose ps

build-image:
	docker compose build

clean:
	docker compose down -v
	rm -rf bin/

# ==============================================================================
# Local Development (本地开发)
# ==============================================================================

# 运行所有单元测试 (带竞态检测)
test:
	go test -v -race ./...

# 静态代码分析 (需安装 golangci-lint)
lint:
	golangci-lint run ./...

# 基础代码检查
vet:
	go vet ./...

# 编译本地二进制文件
build:
	@echo "Building binaries..."
	mkdir -p bin/
	go build -o bin/api ./cmd/api
	go build -o bin/crawler ./cmd/crawler
	@echo "Build complete: bin/api, bin/crawler"

# 运行本地服务 (先编译，然后在独立终端运行)
run: build
	@echo "Starting services locally..."
	@echo "Note: Run these in separate terminals:"
	@echo "  Terminal 1: ./bin/crawler"
	@echo "  Terminal 2: ./bin/api"
	@echo ""
	@echo "Or use 'make run-crawler' and 'make run-api' in separate terminals"

# 运行 API 服务
run-api: build
	@echo "Starting API server..."
	./bin/api

# 运行 Crawler 服务
run-crawler: build
	@echo "Starting Crawler service..."
	./bin/crawler

# 开发模式：同时运行两个服务
dev:
	@./scripts/dev.sh

# 停止开发服务
stop-dev:
	@./scripts/stop_dev.sh

# 整理 Go 依赖
tidy:
	go mod tidy

# 生成 gRPC 代码 (需安装 protoc)
proto:
	protoc --go_out=. --go_grpc_out=. proto/*.proto

help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Docker Targets:"
	@echo "  up           Start docker environment"
	@echo "  down         Stop docker environment"
	@echo "  logs         View docker logs"
	@echo "  ps           List docker containers"
	@echo ""
	@echo "Local Development Targets:"
	@echo "  build        Build binaries locally"
	@echo "  run          Build and show run instructions"
	@echo "  run-api      Build and run API server"
	@echo "  run-crawler  Build and run Crawler service"
	@echo "  dev          Start both services in background (recommended)"
	@echo "  stop-dev     Stop background services"
	@echo "  test         Run tests locally"
	@echo "  lint         Run linter"
	@echo "  vet          Run go vet"
	@echo ""
	@echo "Other Targets:"
	@echo "  proto        Generate gRPC code"
	@echo "  tidy         Tidy go.mod"
	@echo "  clean        Clean up docker volumes and binaries"

