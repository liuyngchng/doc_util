#!/usr/bin/env bash
set -euo pipefail

# ────────────────────────────────────────────────────────────────
# 部署 docutil 容器（纯部署，不构建）
#   ./build/deploy.sh              # 部署 server
#   ./build/deploy.sh mcp          # 部署 mcp
#
# 要求镜像已存在（先执行 ./build/build_server.sh 或 build_mcp.sh）。
# 部署完成后自动 docker logs -f 跟踪容器日志。
# ────────────────────────────────────────────────────────────────

SCRIPT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$SCRIPT_DIR"

TARGET="${1:-server}"

case "$TARGET" in
  server)
    exec "$SCRIPT_DIR/deploy/deploy_server.sh"
    ;;
  mcp)
    exec "$SCRIPT_DIR/deploy/deploy_mcp.sh"
    ;;
  -h|--help|help)
    echo "用法: ./build/deploy.sh [server|mcp]"
    echo ""
    echo "  server  部署 docutil-server（默认）"
    echo "  mcp     部署 docutil-mcp"
    echo ""
    echo "部署前请先构建镜像："
    echo "  ./build/build_server.sh"
    echo "  ./build/build_mcp.sh"
    exit 0
    ;;
  *)
    echo "错误: 未知参数 '$TARGET'，可选 server 或 mcp"
    exit 1
    ;;
esac