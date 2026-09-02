#!/usr/bin/env bash
# wb2api 一键抓包工具
#
# 用法:
#   ./capture.sh start   # 启动 mitmdump 抓包（监听 127.0.0.1:8080）
#   ./capture.sh stop    # 停止抓包
#   ./capture.sh help
#
# 启动后按提示运行官方 codebuddy CLI；抓完执行 ./capture.sh stop，
# 把 captures/ 目录里的 JSON 文件交给分析即可。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ADDON="$SCRIPT_DIR/addon.py"
CONFDIR="${WB2API_MITM_CONFDIR:-$HOME/.wb2api-mitm}"
PORT="${WB2API_MITM_PORT:-8080}"
PIDFILE="$CONFDIR/mitmdump.pid"
LOG="$CONFDIR/mitmdump.log"
OUTDIR="${WB2API_CAPTURE_DIR:-$SCRIPT_DIR/captures}"

# 出网方式：默认复用当前 shell 的 http/https 代理（如 127.0.0.1:10808），
# 也可用 WB2API_UPSTREAM_PROXY 覆盖；为空则直连。
UPSTREAM="${WB2API_UPSTREAM_PROXY:-${https_proxy:-${HTTPS_PROXY:-${http_proxy:-}}}}"

ensure_mitm() {
  if command -v mitmdump >/dev/null 2>&1; then
    return
  fi
  echo "==> 未找到 mitmdump，尝试安装 mitmproxy（需要 pip）..."
  python3 -m pip install --user --break-system-packages mitmproxy
  if ! command -v mitmdump >/dev/null 2>&1; then
    echo "!! mitmdump 仍未在 PATH 中，请手动安装后重试："
    echo "   python3 -m pip install --user mitmproxy"
    echo "   并确保 ~/.local/bin 在 PATH 中"
    exit 1
  fi
}

ca_path() {
  echo "$CONFDIR/mitmproxy-ca-cert.pem"
}

start() {
  ensure_mitm
  mkdir -p "$CONFDIR" "$OUTDIR"
  if [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
    echo "!! 抓包已在运行（pid $(cat "$PIDFILE")）"
    print_usage_hint
    return
  fi
  echo "==> 首次运行会在 $CONFDIR 生成 mitmproxy CA 证书..."
  # 先跑一次生成证书
  mitmdump --set confdir="$CONFDIR" --listen-port "$PORT" -s "$ADDON" \
    >/dev/null 2>&1 &
  first_pid=$!
  sleep 2
  kill "$first_pid" 2>/dev/null || true
  wait "$first_pid" 2>/dev/null || true

  MODE_ARGS=()
  if [ -n "$UPSTREAM" ]; then
    echo "==> 出网经上游代理: $UPSTREAM"
    MODE_ARGS=(--mode "upstream:$UPSTREAM")
  else
    echo "==> 直连出网"
  fi

  echo "==> 启动 mitmdump（端口 $PORT，输出到 $OUTDIR）..."
  WB2API_CAPTURE_DIR="$OUTDIR" nohup mitmdump \
    --set confdir="$CONFDIR" \
    --listen-port "$PORT" \
    "${MODE_ARGS[@]}" \
    -s "$ADDON" \
    >"$LOG" 2>&1 &
  echo $! >"$PIDFILE"
  sleep 1
  if ! kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
    echo "!! mitmdump 启动失败，日志见 $LOG"
    exit 1
  fi
  echo "==> 抓包已启动（pid $(cat "$PIDFILE")）"
  print_usage_hint
}

stop() {
  if [ -f "$PIDFILE" ]; then
    kill "$(cat "$PIDFILE")" 2>/dev/null || true
    rm -f "$PIDFILE"
    echo "==> 已停止抓包"
  else
    echo "==> 抓包未在运行"
  fi
  echo "==> 捕获文件位于: $OUTDIR"
  count=$(ls -1 "$OUTDIR" 2>/dev/null | wc -l)
  echo "==> 本次共 $count 个 flow 文件，可直接交给分析。"
}

print_usage_hint() {
  cat <<EOF

========================================================================
  在另一个终端里，用代理跑官方 codebuddy CLI 触发流量：

  export NODE_EXTRA_CA_CERTS=$(ca_path)
  export HTTPS_PROXY=http://127.0.0.1:${PORT}
  export HTTPS_PROXY_FORCE=1
  unset NO_PROXY 2>/dev/null || true
  codebuddy

  操作要点：
  1. 登录（如已登录则跳过）：触发一次登录/刷新
  2. 发起 2~3 轮正常对话（含一次带工具调用、一次长对话）
  3. 对同一段固定前缀重复发 4 次（缓存测试，观察 usage 字段）

  抓完回这里执行 ./capture.sh stop
========================================================================
EOF
}

case "${1:-help}" in
  start) start ;;
  stop) stop ;;
  help|*) cat <<EOF
用法: $0 {start|stop|help}
  start  启动 mitmproxy 抓包（首次自动安装 mitmproxy 并生成 CA）
  stop   停止抓包并列出捕获文件
EOF
  ;;
esac
