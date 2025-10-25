#!/bin/bash

echo "GoodsHunter 启动脚本"
echo "======================="
echo ""

# 检查配置文件
CONFIG_FILE="config.json"
if [ -f "configs/config.json" ]; then
    CONFIG_FILE="configs/config.json"
fi

if [ ! -f "${CONFIG_FILE}" ]; then
    echo "配置文件不存在"
    echo ""
    echo "请执行以下命令创建配置文件:"
    echo "  cp configs/config.json.example ${CONFIG_FILE}"
    echo "  vim ${CONFIG_FILE}  # 修改为实际配置"
    echo ""
    exit 1
fi

echo "配置文件: ${CONFIG_FILE}"

# 验证 JSON 格式
if command -v jq &> /dev/null; then
    if ! jq empty "${CONFIG_FILE}" 2>/dev/null; then
        echo "配置文件 JSON 格式错误"
        echo ""
        echo "请使用以下命令验证:"
        echo "  jq . ${CONFIG_FILE}"
        exit 1
    fi
    echo "JSON 格式正确"
else
    echo "未安装 jq，跳过 JSON 格式验证"
fi

# 显示配置
echo ""
echo "当前配置:"
if command -v jq &> /dev/null; then
    echo "  环境: $(jq -r '.app.env' "${CONFIG_FILE}")"
    echo "  HTTP 端口: $(jq -r '.app.http_addr' "${CONFIG_FILE}")"
    echo "  Worker Pool: $(jq -r '.app.worker_pool_size' "${CONFIG_FILE}")"
    echo "  队列容量: $(jq -r '.app.queue_capacity' "${CONFIG_FILE}")"
    echo "  调度间隔: $(jq -r '.app.schedule_interval' "${CONFIG_FILE}")"
fi

echo ""
echo "编译服务..."
go build -o bin/api ./cmd/api/
if [ $? -ne 0 ]; then
	echo "编译失败"
	exit 1
fi
go build -o bin/crawler ./cmd/crawler/
if [ $? -ne 0 ]; then
	echo "编译失败"
	exit 1
fi
echo "编译成功"

echo ""
echo "启动服务..."
echo "日志输出: logs/api.log / logs/crawler.log"
echo "按 Ctrl+C 停止服务"
echo ""

mkdir -p logs

./bin/api > logs/api.log 2>&1 &
API_PID=$!
echo "${API_PID}" > logs/api.pid

./bin/crawler > logs/crawler.log 2>&1 &
CRAWLER_PID=$!
echo "${CRAWLER_PID}" > logs/crawler.pid

echo "API PID: ${API_PID}"
echo "Crawler PID: ${CRAWLER_PID}"
echo ""
echo "服务已在后台启动"
echo "查看日志: tail -f logs/api.log 或 tail -f logs/crawler.log"
echo "停止服务: ./scripts/stop.sh"
