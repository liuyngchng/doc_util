#!/usr/bin/env bash
set -euo pipefail

# ────────────────────────────────────────────────────────────────
# Build the production Docker image for the headless HTTP server.
#   ./scripts/build_server.sh
#
# The server binary is compiled inside the build container, then
# packaged into a slim production image (ubuntu:24.04 + OpenCV libs).
# ────────────────────────────────────────────────────────────────

SCRIPT_DIR="$(cd "$(dirname "$0")/.." && pwd)"

# ── Usage ───────────────────────────────────────────────────────
usage() {
  cat <<'EOF'
用法: ./scripts/build_server.sh [参数...]

参数说明:
  -x | verbose            输出 go build 的详细编译日志（-x）
  -h | --help             显示本帮助并退出

代理参数（可选，不带 = 时继承环境变量）:
  http_proxy=<url>        设置 HTTP 代理
  https_proxy=<url>       设置 HTTPS 代理
  no_proxy=<url>          设置不走代理的地址列表

示例:
  ./scripts/build_server.sh http_proxy=http://127.0.0.1:7890 https_proxy=http://127.0.0.1:7890

说明:
  直接构建生产 Docker 镜像 pdf2docx-server:latest（Server 始终集成 OCR）。
EOF
}

cd "$SCRIPT_DIR"

if [[ ! -f "go.mod" ]]; then
  echo "ERROR: run this script from the pdf2docx directory"
  exit 1
fi

if ! command -v docker &>/dev/null; then
  echo "ERROR: docker not found"
  exit 1
fi

# ── 参数解析 ────────────────────────────────────────────────────
GO_BUILD_X=""
HTTP_PROXY_VAL=""
HTTPS_PROXY_VAL=""
NO_PROXY_VAL=""

for arg in "$@"; do
  case "$arg" in
    http_proxy=*|HTTP_PROXY=*)   HTTP_PROXY_VAL="${arg#*=}" ;;
    https_proxy=*|HTTPS_PROXY=*) HTTPS_PROXY_VAL="${arg#*=}" ;;
    no_proxy=*|NO_PROXY=*)       NO_PROXY_VAL="${arg#*=}" ;;
    -x|verbose)                  GO_BUILD_X="-x" ;;
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

# ── Build image must exist ──────────────────────────────────────
IMAGE="pdf2docx_build:latest"
if ! docker image inspect "$IMAGE" &>/dev/null; then
  echo "Building image $IMAGE ..."
  docker build "${DOCKER_BUILD_ARGS[@]+"${DOCKER_BUILD_ARGS[@]}"}" -t "$IMAGE" -f scripts/Dockerfile .
  echo "Done."
fi

# ── Persistent Go caches ────────────────────────────────────────
GOCACHE_DIR="$SCRIPT_DIR/build/gocache_dev"
GOMODCACHE_DIR="$SCRIPT_DIR/build/gomodcache"
mkdir -p "$GOCACHE_DIR" "$GOMODCACHE_DIR" "$SCRIPT_DIR/dist"

# ── ONNX Runtime SDK (download if missing; uses proxy env) ───────
"$SCRIPT_DIR/scripts/ensure_onnxruntime.sh" \
  ${HTTP_PROXY_VAL:+"http_proxy=$HTTP_PROXY_VAL"} \
  ${HTTPS_PROXY_VAL:+"https_proxy=$HTTPS_PROXY_VAL"} \
  ${NO_PROXY_VAL:+"no_proxy=$NO_PROXY_VAL"}

ONNXRT_LIB="$SCRIPT_DIR/build/deps/onnxruntime"

# ── 1. Build binary in build container ─────────────────────────
echo ""
echo "=== Step 1/2: Building pdf2docx-server binary ==="
docker run --rm \
  -v "$SCRIPT_DIR":/workspace \
  -v "$GOCACHE_DIR":/tmp/gocache \
  -v "$GOMODCACHE_DIR":/go/pkg/mod \
  -w /workspace \
  "${DOCKER_RUN_ENV[@]+"${DOCKER_RUN_ENV[@]}"}" \
  -e GOFLAGS="-buildvcs=false" \
  -e GOCACHE=/tmp/gocache \
  -e GOMODCACHE=/go/pkg/mod \
  -e GOPROXY="https://goproxy.cn,direct" \
  -e CGO_ENABLED=1 \
  -e CGO_LDFLAGS="-L/workspace/build/deps/onnxruntime/lib -lonnxruntime" \
  -e CGO_CFLAGS="-I/workspace/build/deps/onnxruntime/include" \
  -e GOOS=linux \
  -e GOARCH=amd64 \
  "$IMAGE" \
  bash -c "
    rm -rf /workspace/dist/pdf2docx-server
    go build ${GO_BUILD_X} -mod=mod -ldflags='-s -w -r \$ORIGIN' -o dist/pdf2docx-server ./cmd/server/ && \
    echo 'Build complete.'
  "

echo "  dist/pdf2docx-server ($(du -h "$SCRIPT_DIR/dist/pdf2docx-server" | cut -f1))"

# ── 2. Bundle deps + package Docker image ───────────────────────
rm -rf "$SCRIPT_DIR"/dist/libonnxruntime.so* "$SCRIPT_DIR/dist/models"

cp -a "$ONNXRT_LIB/lib/libonnxruntime.so"* "$SCRIPT_DIR/dist/"
cp -r "$SCRIPT_DIR/models" "$SCRIPT_DIR/dist/models"

# Generate self-signed TLS certificate (idempotent: skips if already present).
"$SCRIPT_DIR/scripts/gen_cert.sh"
cp -a "$SCRIPT_DIR/certs/cert.pem" "$SCRIPT_DIR/certs/key.pem" "$SCRIPT_DIR/dist/"

echo ""
echo "=== Step 2/2: Building Docker image pdf2docx-server:latest ==="
docker build "${DOCKER_BUILD_ARGS[@]+"${DOCKER_BUILD_ARGS[@]}"}" -t pdf2docx-server:latest -f scripts/Dockerfile.server "$SCRIPT_DIR"

# Clean up intermediate files from dist/ (keep certs/ as durable assets).
rm -rf "$SCRIPT_DIR/dist/pdf2docx-server" "$SCRIPT_DIR"/dist/libonnxruntime.so* "$SCRIPT_DIR/dist/models" "$SCRIPT_DIR"/dist/cert.pem "$SCRIPT_DIR"/dist/key.pem

echo ""
echo "=== Done ==="
echo "  Docker image: pdf2docx-server:latest"
echo ""
echo "Run with:"
echo "  docker run -d --rm --name pdf2docx-server -p 8443:8080 \\"
echo "    -v \$(pwd)/logs:/app/logs \\"
echo "    pdf2docx-server:latest"
echo "  docker logs -f pdf2docx-server"
echo ""
echo "HTTPS enabled by default (self-signed cert for localhost)."
echo "  Health check: curl -k https://localhost:8443/health"
echo "  Logs on host:  cat logs/server.log"