#!/bin/bash

echo "GoodsHunter 停止脚本"
echo "======================="
echo ""

stop_by_pid_file() {
  local name="$1"
  local pid_file="$2"
  if [ -f "$pid_file" ]; then
    local pid
    pid=$(cat "$pid_file")
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
      echo "停止 ${name} (PID: ${pid})"
      kill "$pid"
      rm -f "$pid_file"
      return
    fi
  fi
  echo "${name} 未发现 PID 文件或进程不存在"
}

stop_by_pgrep() {
  local name="$1"
  local pattern="$2"
  local pids
  pids=$(pgrep -f "$pattern")
  if [ -n "$pids" ]; then
    echo "停止 ${name} (PID: ${pids})"
    kill $pids
    sleep 1
    # 如果进程仍在运行，强制杀死
    pids=$(pgrep -f "$pattern")
    if [ -n "$pids" ]; then
      echo "强制停止 ${name} (PID: ${pids})"
      kill -9 $pids
    fi
  else
    echo "未发现 ${name} 进程"
  fi
}

stop_by_pid_file "API" "logs/api.pid"
stop_by_pid_file "Crawler" "logs/crawler.pid"
stop_by_pgrep "API" "bin/api"
stop_by_pgrep "Crawler" "bin/crawler"

# 额外检查端口占用
echo ""
echo "检查端口占用..."
API_PORT_PID=$(lsof -ti :8081 2>/dev/null)
if [ -n "$API_PORT_PID" ]; then
  echo "清理占用 8081 端口的进程 (PID: ${API_PORT_PID})"
  kill $API_PORT_PID 2>/dev/null || kill -9 $API_PORT_PID 2>/dev/null
fi

CRAWLER_PORT_PID=$(lsof -ti :50051 2>/dev/null)
if [ -n "$CRAWLER_PORT_PID" ]; then
  echo "清理占用 50051 端口的进程 (PID: ${CRAWLER_PORT_PID})"
  kill $CRAWLER_PORT_PID 2>/dev/null || kill -9 $CRAWLER_PORT_PID 2>/dev/null
fi

echo ""
echo "完成"
