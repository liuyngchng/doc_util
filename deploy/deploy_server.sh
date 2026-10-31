#!/usr/bin/env bash
set -euo pipefail

# ────────────────────────────────────────────────────────────────
# 部署 docutil HTTP 服务，参考 llm_agent 的部署方式：
#   - 复用虚拟网桥 llm_net，与其他 llm_agent 服务互通
#   - 以容器名 doc_util 注册到网桥，供网桥内其他容器访问
#
# 使用前先构建镜像：
#   ./build/build_server.sh
#
# 运行：
#   ./deploy/deploy_server.sh
# ────────────────────────────────────────────────────────────────

CONTAINER="doc_util"
SCRIPT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
VERSION="$(cat "$SCRIPT_DIR/VERSION")"
IMAGE="doc_util:$VERSION"

# 确保镜像存在
if ! docker image inspect "$IMAGE" &>/dev/null; then
  echo "错误：镜像 $IMAGE 不存在，请先执行 ./build/build_server.sh"
  exit 1
fi

# 创建（或复用）虚拟网桥，失败（已存在）不中断
docker network create llm_net 2>/dev/null || true

# 停止并删除旧容器（幂等）
docker stop "$CONTAINER" 2>/dev/null || true
docker rm "$CONTAINER" 2>/dev/null || true

echo "正在创建容器 $CONTAINER ..."
docker run -dit --name "$CONTAINER" \
  --restart unless-stopped \
  --security-opt seccomp=unconfined \
  --network llm_net \
  -v "$SCRIPT_DIR/logs:/app/logs" \
  -p 20002:8080 \
  "$IMAGE"

echo "容器 $CONTAINER 已启动"
docker ps -a | grep "$CONTAINER" --color=always
echo "部署完成"

# 显示 llm_net 网络内的访问地址（容器名 = DNS 主机名）
# 注意：TLS 默认启用（自签名证书），证书 SAN 为 localhost，其他容器需 --insecure
echo -e "\n\033[1;31m══════════════════════════════════════════════════════════════\033[0m"
echo -e "\033[1;31m  llm_net 网络内访问地址（同宿主机其他容器可用）：          \033[0m"
echo -e "\033[1;31m      https://doc_util:8080                           \033[0m"
echo -e "\033[1;31m  注意：证书为自签名（SAN: localhost），客户端需 -k/--insecure\033[0m"
echo -e "\033[1;31m      curl -k https://doc_util:8080/health            \033[0m"
echo -e "\033[1;31m══════════════════════════════════════════════════════════════\033[0m\n"

docker logs -f "$CONTAINER"
