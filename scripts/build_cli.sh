#!/usr/bin/env bash
set -euo pipefail

# ────────────────────────────────────────────────────────────────
# Build the GUI client binaries (Linux + Windows).
#   ./scripts/build_cli.sh                 # Linux GUI with OCR
#
# Linux binary is built with full OCR support (OpenCV + ONNX Runtime).
# Windows cross-compile uses -tags noocr (no OpenCV cross-link), but
# MuPDF text extraction is always available for text-mode DOCX output.
#
# Windows native OCR build must be done on Windows:
#   scripts/build.bat    (requires MSYS2 UCRT64)
# ────────────────────────────────────────────────────────────────

SCRIPT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
IMAGE="docutil_build:latest"

# ── Usage ───────────────────────────────────────────────────────
usage() {
  cat <<'EOF'
用法: ./scripts/build_cli.sh [参数...]

参数说明:
  --no-obfuscate          关闭 Windows 构建的 garble 代码混淆（默认开启）
  -x | verbose            输出 go build 的详细编译日志（-x）
  -h | --help             显示本帮助并退出

代理参数（可选，均不带 = 时也会继承环境变量）:
  http_proxy=<url>        设置 HTTP 代理
  https_proxy=<url>       设置 HTTPS 代理
  no_proxy=<url>          设置不走代理的地址列表

示例:
  ./scripts/build_cli.sh                  # 默认构建（Linux OCR + Windows 混淆）
  ./scripts/build_cli.sh --no-obfuscate   # 不混淆 Windows 构建

说明:
  Linux 构建始终集成 OCR（OpenCV + ONNX Runtime），产出含 .so + models 的完整包。
  Windows 交叉编译始终使用 -tags noocr（不支持 Windows OCR 交叉编译）。
  Windows OCR 版本需在 Windows 上运行 scripts/build.bat 编译（需要 MSYS2 UCRT64）。
EOF
}

# ── Optional proxy ──────────────────────────────────────────────
HTTP_PROXY_VAL=""
HTTPS_PROXY_VAL=""
NO_PROXY_VAL=""
GO_BUILD_X=""   # set to "-x" when verbose is requested
OBFUSCATE=true
for arg in "$@"; do
  case "$arg" in
    http_proxy=*|HTTP_PROXY=*)   HTTP_PROXY_VAL="${arg#*=}" ;;
    https_proxy=*|HTTPS_PROXY=*) HTTPS_PROXY_VAL="${arg#*=}" ;;
    no_proxy=*|NO_PROXY=*)       NO_PROXY_VAL="${arg#*=}" ;;
    -x|verbose)                  GO_BUILD_X="-x" ;;
    --no-obfuscate)              OBFUSCATE=false ;;
    -h|--help|help)              usage; exit 0 ;;
    *)                           echo "WARNING: 未知参数 '$arg'，已忽略（用 -h 查看用法）" ;;
  esac
done

# 打印参数用法说明，便于在日志里看到各参数含义
if [[ -t 1 ]]; then
  usage
fi

# ── 总是输出当前构建参数（日志中可见） ────────────────────────────
echo ""
echo "────────────────────────────────────────────────────────"
echo "  构建参数:"
echo "    OBFUSCATE      = $OBFUSCATE    (仅 Windows 构建)"
echo "    GO_BUILD_X     = ${GO_BUILD_X:-(未设置)}"
echo "    HTTP_PROXY     = ${HTTP_PROXY_VAL:-(未设置)}"
echo "    HTTPS_PROXY    = ${HTTPS_PROXY_VAL:-(未设置)}"
echo "────────────────────────────────────────────────────────"
echo ""
HTTP_PROXY_VAL="${HTTP_PROXY_VAL:-${HTTP_PROXY:-${http_proxy:-}}}"
HTTPS_PROXY_VAL="${HTTPS_PROXY_VAL:-${HTTPS_PROXY:-${https_proxy:-}}}"
NO_PROXY_VAL="${NO_PROXY_VAL:-${NO_PROXY:-${no_proxy:-}}}"

add_scheme() { local v="$1"; [[ -z "$v" || "$v" == *"://"* ]] && { printf '%s' "$v"; return; }; printf 'http://%s' "$v"; }
HTTP_PROXY_VAL="$(add_scheme "$HTTP_PROXY_VAL")"
HTTPS_PROXY_VAL="$(add_scheme "$HTTPS_PROXY_VAL")"

DOCKER_BUILD_ARGS=()
DOCKER_RUN_ENV=()
if [[ -n "$HTTP_PROXY_VAL" || -n "$HTTPS_PROXY_VAL" ]]; then
  export HTTP_PROXY="$HTTP_PROXY_VAL" HTTPS_PROXY="$HTTPS_PROXY_VAL"
  export http_proxy="$HTTP_PROXY_VAL" https_proxy="$HTTPS_PROXY_VAL"
  export NO_PROXY="$NO_PROXY_VAL"     no_proxy="$NO_PROXY_VAL"
  DOCKER_BUILD_ARGS=(--build-arg "HTTP_PROXY=$HTTP_PROXY_VAL" --build-arg "HTTPS_PROXY=$HTTPS_PROXY_VAL" --build-arg "NO_PROXY=$NO_PROXY_VAL")
  DOCKER_RUN_ENV=(-e "HTTP_PROXY=$HTTP_PROXY_VAL" -e "HTTPS_PROXY=$HTTPS_PROXY_VAL" -e "NO_PROXY=$NO_PROXY_VAL" -e "http_proxy=$HTTP_PROXY_VAL" -e "https_proxy=$HTTPS_PROXY_VAL" -e "no_proxy=$NO_PROXY_VAL")
fi

cd "$SCRIPT_DIR"

if [[ ! -f "main.go" ]] || [[ ! -f "go.mod" ]]; then
  echo "ERROR: run this script from the docutil directory"
  exit 1
fi

if ! command -v docker &>/dev/null; then
  echo "ERROR: docker not found"
  exit 1
fi

# ── Cache directories (mapped as volumes for speed) ──────────────
GOCACHE_DIR="$SCRIPT_DIR/build/gocache"
GOMODCACHE_DIR="$SCRIPT_DIR/build/gomodcache"
mkdir -p "$GOCACHE_DIR" "$GOMODCACHE_DIR" "$SCRIPT_DIR/dist"

# ── ONNX Runtime SDK (download if missing; uses proxy env) ───────
"$SCRIPT_DIR/scripts/ensure_onnxruntime.sh" \
  ${HTTP_PROXY_VAL:+"http_proxy=$HTTP_PROXY_VAL"} \
  ${HTTPS_PROXY_VAL:+"https_proxy=$HTTPS_PROXY_VAL"}

# ── Go toolchain tarball (download if missing) ───────────────────
GO_VERSION="1.24.13"
GO_TARBALL="$SCRIPT_DIR/build/deps/go${GO_VERSION}.linux-amd64.tar.gz"
GO_DOWNLOAD_URL="https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz"

if [[ ! -f "$GO_TARBALL" ]]; then
  mkdir -p "$(dirname "$GO_TARBALL")"
  echo "Downloading Go ${GO_VERSION} toolchain ..."
  if command -v curl &>/dev/null; then
    curl -fL --retry 3 -o "$GO_TARBALL" "$GO_DOWNLOAD_URL"
  elif command -v wget &>/dev/null; then
    wget -O "$GO_TARBALL" "$GO_DOWNLOAD_URL"
  else
    echo "ERROR: neither curl nor wget found; cannot download Go toolchain"
    exit 1
  fi
  echo "Go toolchain saved to $GO_TARBALL"
else
  echo "Go toolchain tarball ready: $GO_TARBALL"
fi

# ── Build Docker image if missing ────────────────────────────────
if ! docker image inspect "$IMAGE" &>/dev/null; then
  echo "Building Docker image $IMAGE ..."
  docker build "${DOCKER_BUILD_ARGS[@]}" --build-arg "GO_VERSION=$GO_VERSION" -t "$IMAGE" -f scripts/Dockerfile .
  echo "Docker image $IMAGE built"
else
  echo "Docker image $IMAGE ready"
fi

COMMON_ENV=(
  -e GOFLAGS="-buildvcs=false"
  -e GOCACHE=/tmp/gocache
  -e GOMODCACHE=/go/pkg/mod
  -e GOPROXY="https://goproxy.cn,direct"
  -e HOST_UID="$(id -u)"
  -e HOST_GID="$(id -g)"
)

# ── Obfuscation (Windows only; Linux OCR build cannot use garble) ───
if $OBFUSCATE; then
  WIN_BUILD_BIN="garble -literals"
  GARBLE_ENV=(-e GOGARBLE=docutil)
  WIN_OBF_LABEL=" (obfuscated)"
else
  WIN_BUILD_BIN="go"
  GARBLE_ENV=()
  WIN_OBF_LABEL=""
fi

# ── 1. Linux GUI (always OCR) ────────────────────────────────────
echo ""
echo "=== Building docutil (Linux GUI, OCR) ==="
docker run --rm \
  -v "$SCRIPT_DIR":/workspace \
  -v "$GOCACHE_DIR":/tmp/gocache \
  -v "$GOMODCACHE_DIR":/go/pkg/mod \
  -w /workspace \
  "${COMMON_ENV[@]}" \
  ${DOCKER_RUN_ENV[@]+"${DOCKER_RUN_ENV[@]}"} \
  -e CGO_ENABLED=1 \
  -e GOOS=linux \
  -e GOARCH=amd64 \
  "$IMAGE" \
  bash -c "
    rm -rf /workspace/dist/*
    mkdir -p /workspace/dist
    go build ${GO_BUILD_X} -mod=mod -ldflags='-s -w' -o dist/docutil . && \
    chown \$HOST_UID:\$HOST_GID dist/docutil
  "

# ── Bundle OCR runtime deps ──────────────────────────────────────
# Copy onnxruntime from vendored deps (preserve soname symlinks).
cp -a "$SCRIPT_DIR/build/deps/onnxruntime/lib/libonnxruntime.so"* "$SCRIPT_DIR/dist/"
echo "  bundled:  dist/libonnxruntime.so*"

# Copy OpenCV shared libs + patchelf rpath from the build image.
docker run --rm -v "$SCRIPT_DIR":/workspace -w /workspace \
  -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" \
  "$IMAGE" bash -c '
  set -euo pipefail
  LIBDIR=/usr/lib/x86_64-linux-gnu
  mkdir -p /workspace/dist
  for lib in libopencv_imgproc.so.406 libopencv_core.so.406 libtbb.so.12; do
    cp -L "$LIBDIR/$lib" /workspace/dist/
    patchelf --set-rpath "\$ORIGIN" "/workspace/dist/$lib"
    echo "  bundled:  dist/$lib"
  done
  chown "$HOST_UID":"$HOST_GID" /workspace/dist/libopencv_*.so.406 /workspace/dist/libtbb.so.12
'

# Copy OCR model files.
cp -r "$SCRIPT_DIR/models" "$SCRIPT_DIR/dist/models"
echo "  bundled:  dist/models/"

# ── 2. Windows GUI (cross-compile, no OCR) ───────────────────────
echo ""
echo "=== Building docutil.exe (Windows GUI)${WIN_OBF_LABEL} ==="
docker run --rm \
  -v "$SCRIPT_DIR":/workspace \
  -v "$GOCACHE_DIR":/tmp/gocache \
  -v "$GOMODCACHE_DIR":/go/pkg/mod \
  -w /workspace \
  "${COMMON_ENV[@]}" \
  ${DOCKER_RUN_ENV[@]+"${DOCKER_RUN_ENV[@]}"} \
  ${GARBLE_ENV[@]+"${GARBLE_ENV[@]}"} \
  -e CGO_ENABLED=1 \
  -e GOOS=windows \
  -e GOARCH=amd64 \
  -e CC=x86_64-w64-mingw32-gcc \
  -e CGO_LDFLAGS="-lucrt" \
  "$IMAGE" \
  bash -c "
    rm -rf /workspace/dist/docutil.exe
    ${WIN_BUILD_BIN} build ${GO_BUILD_X} -mod=mod -tags noocr -ldflags='-s -w -H windowsgui' -o dist/docutil.exe . && \
    chown \$HOST_UID:\$HOST_GID dist/docutil.exe
  "

echo ""
echo "=== Build complete ==="
echo "  Linux GUI:    dist/docutil      ($(du -h "$SCRIPT_DIR/dist/docutil" | cut -f1))"
echo "  Windows GUI:  dist/docutil.exe  ($(du -h "$SCRIPT_DIR/dist/docutil.exe" | cut -f1))"

# ── Package into tar for distribution ─────────────────────────────
echo ""
echo "--- Packaging ---"
ARCHIVE_LINUX="docutil-linux-amd64.tar"
ARCHIVE_WIN="docutil-windows-amd64.tar"

rm -f "$SCRIPT_DIR/dist/$ARCHIVE_LINUX" "$SCRIPT_DIR/dist/$ARCHIVE_WIN"

# Linux: always bundle .so + models (OCR version).
# Stage files into a directory first (tar --transform would also rewrite
# symlink targets, breaking libonnxruntime.so -> .so.1 -> .so.1.27.1).
STAGE_DIR="$SCRIPT_DIR/dist/docutil-linux-amd64"
rm -rf "$STAGE_DIR"
mkdir -p "$STAGE_DIR"
cp -a \
  "$SCRIPT_DIR/dist/docutil" \
  "$SCRIPT_DIR"/dist/libonnxruntime.so* \
  "$SCRIPT_DIR/dist/libopencv_imgproc.so.406" \
  "$SCRIPT_DIR/dist/libopencv_core.so.406" \
  "$SCRIPT_DIR/dist/libtbb.so.12" \
  "$SCRIPT_DIR/dist/models" \
  "$STAGE_DIR/"
tar -cf "$SCRIPT_DIR/dist/$ARCHIVE_LINUX" -C "$SCRIPT_DIR/dist" docutil-linux-amd64
rm -rf "$STAGE_DIR"
echo "  $ARCHIVE_LINUX  ($(du -h "$SCRIPT_DIR/dist/$ARCHIVE_LINUX" | cut -f1)) — Linux OCR 完整包"

rm -rf \
  "$SCRIPT_DIR/dist/docutil" \
  "$SCRIPT_DIR"/dist/libonnxruntime.so* \
  "$SCRIPT_DIR/dist/libopencv_imgproc.so.406" "$SCRIPT_DIR/dist/libopencv_core.so.406" \
  "$SCRIPT_DIR/dist/libtbb.so.12" \
  "$SCRIPT_DIR/dist/models"

# Windows: single exe (text extraction via MuPDF only, no OCR).
cd "$SCRIPT_DIR/dist"
tar -cf "$ARCHIVE_WIN" --transform='s,^,docutil-windows-amd64/,' docutil.exe
cd "$SCRIPT_DIR"
echo "  $ARCHIVE_WIN  ($(du -h "$SCRIPT_DIR/dist/$ARCHIVE_WIN" | cut -f1))"
rm -f "$SCRIPT_DIR/dist/docutil.exe"

echo ""
echo "分发文件（做好了的压缩包）:"
echo "  dist/$ARCHIVE_LINUX"
echo "  dist/$ARCHIVE_WIN"
echo ""
echo "Linux OCR 包内容: docutil + .so 库 + models/ 模型目录"
echo "解包后进入 docutil-linux-amd64/ 目录，运行 ./docutil 即可使用。"
echo "Windows 包内容: docutil.exe（支持 MuPDF 文本提取，OCR 请用 scripts/build.bat 原生编译）"