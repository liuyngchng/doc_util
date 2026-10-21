#!/usr/bin/env bash
# Launch a dev container for OCR development.
# Maps the project directory into /workspace, same as build_cli.sh.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$SCRIPT_DIR"
IMAGE="docutil_build:latest"

# ── Usage ───────────────────────────────────────────────────────
usage() {
  cat <<'EOF'
用法: ./scripts/dev.sh [代理参数...]

启动 OCR 开发容器（交互式 bash），挂载项目目录到 /workspace。

代理参数（可选，不带 = 时继承环境变量）:
  http_proxy=<url>        设置 HTTP 代理
  https_proxy=<url>       设置 HTTPS 代理
  no_proxy=<url>          设置不走代理的地址列表

示例:
  ./scripts/dev.sh
  ./scripts/dev.sh http_proxy=http://127.0.0.1:7890 https_proxy=http://127.0.0.1:7890
EOF
}

# ── 参数解析 ────────────────────────────────────────────────────
HTTP_PROXY_VAL=""
HTTPS_PROXY_VAL=""
NO_PROXY_VAL=""

for arg in "$@"; do
  case "$arg" in
    http_proxy=*|HTTP_PROXY=*)   HTTP_PROXY_VAL="${arg#*=}" ;;
    https_proxy=*|HTTPS_PROXY=*) HTTPS_PROXY_VAL="${arg#*=}" ;;
    no_proxy=*|NO_PROXY=*)       NO_PROXY_VAL="${arg#*=}" ;;
    -h|--help|help)              usage; exit 0 ;;
    *)                           echo "WARNING: 未知参数 '$arg'，已忽略（用 -h 查看用法）" ;;
  esac
done

# 不带 = 时继承环境变量
HTTP_PROXY_VAL="${HTTP_PROXY_VAL:-${HTTP_PROXY:-${http_proxy:-}}}"
HTTPS_PROXY_VAL="${HTTPS_PROXY_VAL:-${HTTPS_PROXY:-${https_proxy:-}}}"
NO_PROXY_VAL="${NO_PROXY_VAL:-${NO_PROXY:-${no_proxy:-}}}"

add_scheme() { local v="$1"; [[ -z "$v" || "$v" == *"://"* ]] && { printf '%s' "$v"; return; }; printf 'http://%s' "$v"; }
HTTP_PROXY_VAL="$(add_scheme "$HTTP_PROXY_VAL")"
HTTPS_PROXY_VAL="$(add_scheme "$HTTPS_PROXY_VAL")"

if [[ -n "$HTTP_PROXY_VAL" || -n "$HTTPS_PROXY_VAL" ]]; then
  export HTTP_PROXY="$HTTP_PROXY_VAL" HTTPS_PROXY="$HTTPS_PROXY_VAL"
  export http_proxy="$HTTP_PROXY_VAL" https_proxy="$HTTPS_PROXY_VAL"
  export NO_PROXY="$NO_PROXY_VAL"     no_proxy="$NO_PROXY_VAL"
  echo "代理: http=$HTTP_PROXY_VAL https=$HTTPS_PROXY_VAL no_proxy=$NO_PROXY_VAL"
fi

# ── Docker build arg / run env ───────────────────────────────────
DOCKER_BUILD_ARGS=()
DOCKER_RUN_ENV=()
if [[ -n "$HTTP_PROXY_VAL" || -n "$HTTPS_PROXY_VAL" ]]; then
  DOCKER_BUILD_ARGS=(--build-arg "HTTP_PROXY=$HTTP_PROXY_VAL" --build-arg "HTTPS_PROXY=$HTTPS_PROXY_VAL" --build-arg "NO_PROXY=$NO_PROXY_VAL")
  DOCKER_RUN_ENV=(
    -e "HTTP_PROXY=$HTTP_PROXY_VAL" -e "HTTPS_PROXY=$HTTPS_PROXY_VAL" -e "NO_PROXY=$NO_PROXY_VAL"
    -e "http_proxy=$HTTP_PROXY_VAL" -e "https_proxy=$HTTPS_PROXY_VAL" -e "no_proxy=$NO_PROXY_VAL"
  )
fi

if [[ -t 1 ]]; then
  usage
fi

# ── ONNX Runtime SDK (download if missing; uses proxy env) ───────
"$SCRIPT_DIR/scripts/ensure_onnxruntime.sh" \
  ${HTTP_PROXY_VAL:+"http_proxy=$HTTP_PROXY_VAL"} \
  ${HTTPS_PROXY_VAL:+"https_proxy=$HTTPS_PROXY_VAL"} \
  ${NO_PROXY_VAL:+"no_proxy=$NO_PROXY_VAL"}

# Build dev image if not present
if ! docker image inspect "$IMAGE" &>/dev/null; then
  echo "Building dev image $IMAGE ..."
  docker build "${DOCKER_BUILD_ARGS[@]+"${DOCKER_BUILD_ARGS[@]}"}" -t "$IMAGE" -f scripts/Dockerfile .
  echo "Done."
fi

GOCACHE_DIR="$SCRIPT_DIR/build/gocache_dev"
GOMODCACHE_DIR="$SCRIPT_DIR/build/gomodcache"
mkdir -p "$GOCACHE_DIR" "$GOMODCACHE_DIR"

echo "Starting dev container. Use 'go build' or 'go test' inside."
echo "  CGO LDFLAGS:  -L/workspace/build/deps/onnxruntime/lib -lonnxruntime"
echo "  CGO CFLAGS:   -I/workspace/build/deps/onnxruntime/include"
echo "  OpenCV:       pkg-config --cflags --libs opencv4"
echo ""

docker run --rm -it \
  -v "$SCRIPT_DIR":/workspace \
  -v "$GOCACHE_DIR":/tmp/gocache \
  -v "$GOMODCACHE_DIR":/go/pkg/mod \
  -w /workspace \
  "${DOCKER_RUN_ENV[@]+"${DOCKER_RUN_ENV[@]}"}" \
  -e GOCACHE=/tmp/gocache \
  -e GOMODCACHE=/go/pkg/mod \
  -e GOPROXY=https://goproxy.cn,direct \
  "$IMAGE" \
  bash