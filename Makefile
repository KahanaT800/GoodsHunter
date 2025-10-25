.PHONY: all up down logs ps build clean test lint vet help

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

# 整理 Go 依赖
tidy:
	go mod tidy

# 生成 gRPC 代码 (需安装 protoc)
proto:
	protoc --go_out=. --go_grpc_out=. proto/*.proto

help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@echo "  up           Start docker environment"
	@echo "  down         Stop docker environment"
	@echo "  test         Run tests locally"
	@echo "  lint         Run linter"
	@echo "  build        Build binaries locally"
	@echo "  proto        Generate gRPC code"

