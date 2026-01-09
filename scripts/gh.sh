#!/bin/bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOG_DIR="${ROOT_DIR}/logs"

usage() {
  cat <<'EOF'
GoodsHunter script manager

Usage:
  ./scripts/gh.sh <command> [options]

Commands:
  dev-up [--follow]    Build and start API/Crawler locally
  dev-down             Stop local API/Crawler processes
  dev-logs             Follow local API/Crawler logs
  compose-up           docker compose up -d
  compose-down         docker compose down
  clean-db             Truncate task-related tables in MySQL

Examples:
  ./scripts/gh.sh dev-up --follow
  ./scripts/gh.sh dev-down
  ./scripts/gh.sh compose-up
EOF
}

load_env() {
  if [ -f "${ROOT_DIR}/.env" ]; then
    set -a
    # shellcheck disable=SC1090
    source "${ROOT_DIR}/.env"
    set +a
  fi
}

config_file() {
  if [ -f "${ROOT_DIR}/configs/config.json" ]; then
    echo "${ROOT_DIR}/configs/config.json"
  else
    echo "${ROOT_DIR}/config.json"
  fi
}

check_config() {
  local cfg
  cfg="$(config_file)"
  if [ ! -f "${cfg}" ]; then
    echo "配置文件不存在: ${cfg}"
    echo "请执行: cp configs/config.json.example ${cfg}"
    exit 1
  fi
  if command -v jq >/dev/null 2>&1; then
    if ! jq empty "${cfg}" 2>/dev/null; then
      echo "配置文件 JSON 格式错误: ${cfg}"
      exit 1
    fi
  fi
}

build_binaries() {
  if [ -f "${ROOT_DIR}/Makefile" ] && command -v make >/dev/null 2>&1; then
    make -C "${ROOT_DIR}" build
  else
    mkdir -p "${ROOT_DIR}/bin"
    (cd "${ROOT_DIR}" && go build -o bin/api ./cmd/api/)
    (cd "${ROOT_DIR}" && go build -o bin/crawler ./cmd/crawler/)
  fi
}

stop_local() {
  local pid
  if [ -f "${LOG_DIR}/api.pid" ]; then
    pid="$(cat "${LOG_DIR}/api.pid")"
    if [ -n "${pid}" ] && kill -0 "${pid}" 2>/dev/null; then
      echo "停止 API (PID: ${pid})"
      kill "${pid}" || true
    fi
    rm -f "${LOG_DIR}/api.pid"
  fi
  if [ -f "${LOG_DIR}/crawler.pid" ]; then
    pid="$(cat "${LOG_DIR}/crawler.pid")"
    if [ -n "${pid}" ] && kill -0 "${pid}" 2>/dev/null; then
      echo "停止 Crawler (PID: ${pid})"
      kill "${pid}" || true
    fi
    rm -f "${LOG_DIR}/crawler.pid"
  fi
  pkill -f "bin/api" 2>/dev/null || true
  pkill -f "bin/crawler" 2>/dev/null || true
}

dev_up() {
  local follow="${1:-}"
  check_config
  build_binaries
  stop_local
  mkdir -p "${LOG_DIR}"
  echo "启动 Crawler..."
  "${ROOT_DIR}/bin/crawler" > "${LOG_DIR}/crawler.log" 2>&1 &
  echo $! > "${LOG_DIR}/crawler.pid"
  sleep 2
  echo "启动 API..."
  "${ROOT_DIR}/bin/api" > "${LOG_DIR}/api.log" 2>&1 &
  echo $! > "${LOG_DIR}/api.pid"
  echo "API 日志: ${LOG_DIR}/api.log"
  echo "Crawler 日志: ${LOG_DIR}/crawler.log"
  if [ "${follow}" = "--follow" ]; then
    tail -f "${LOG_DIR}/api.log" "${LOG_DIR}/crawler.log"
  fi
}

dev_logs() {
  if [ ! -d "${LOG_DIR}" ]; then
    echo "日志目录不存在: ${LOG_DIR}"
    exit 1
  fi
  tail -f "${LOG_DIR}/api.log" "${LOG_DIR}/crawler.log"
}

clean_db() {
  load_env
  if [ -z "${MYSQL_ROOT_PASSWORD:-}" ]; then
    read -rsp "请输入 MYSQL_ROOT_PASSWORD: " MYSQL_ROOT_PASSWORD
    echo ""
  fi
  echo "警告：此操作将清空所有任务、商品和关联数据！"
  read -r -p "确认继续？(yes/no): " confirm
  if [ "${confirm}" != "yes" ]; then
    echo "操作已取消"
    exit 0
  fi
  docker compose exec -T mysql mysql -uroot -p"${MYSQL_ROOT_PASSWORD}" --default-character-set=utf8mb4 -e "
    USE goodshunter;
    SET FOREIGN_KEY_CHECKS = 0;
    TRUNCATE TABLE task_items;
    TRUNCATE TABLE items;
    TRUNCATE TABLE tasks;
    SET FOREIGN_KEY_CHECKS = 1;
    ALTER TABLE tasks AUTO_INCREMENT = 1;
    ALTER TABLE items AUTO_INCREMENT = 1;
    ALTER TABLE task_items AUTO_INCREMENT = 1;
  "
  echo "数据库清理完成！"
}

case "${1:-}" in
  dev-up)
    dev_up "${2:-}"
    ;;
  dev-down)
    stop_local
    ;;
  dev-logs)
    dev_logs
    ;;
  compose-up)
    docker compose up -d
    ;;
  compose-down)
    docker compose down
    ;;
  clean-db)
    clean_db
    ;;
  -h|--help|"")
    usage
    ;;
  *)
    echo "未知命令: ${1}"
    usage
    exit 1
    ;;
esac
