#!/usr/bin/env bash
# 确保 ONNX Runtime SDK（头文件 + libonnxruntime.so）已就绪。
#   - 已存在（include/onnxruntime_c_api.h + lib/libonnxruntime.so）则直接使用；
#   - 缺失则自动下载 Linux x64 预编译包并解压到 dist/cache/deps/onnxruntime/。
#
# 代理：优先读取参数 http_proxy=/https_proxy=，否则继承环境变量；
#       curl / wget 也会自动读取 http_proxy / https_proxy。
#
# 被 build_cli.sh / build_server.sh / dev.sh 调用，也可独立运行：
#   ./build/ensure_onnxruntime.sh [http_proxy=<url>] [https_proxy=<url>]
set -euo pipefail

ORT_VERSION="1.27.1"
SCRIPT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
ORT_DIR="$SCRIPT_DIR/dist/cache/deps/onnxruntime"
ORT_HEADER="$ORT_DIR/include/onnxruntime_c_api.h"
ORT_LIB="$ORT_DIR/lib/libonnxruntime.so"
ORT_TARBALL="$SCRIPT_DIR/dist/cache/deps/onnxruntime-linux-x64-${ORT_VERSION}.tgz"
ORT_URL="https://github.com/microsoft/onnxruntime/releases/download/v${ORT_VERSION}/onnxruntime-linux-x64-${ORT_VERSION}.tgz"

# 已就绪则直接使用
if [[ -f "$ORT_HEADER" && -f "$ORT_LIB" ]]; then
  echo "ONNX Runtime ${ORT_VERSION} ready: dist/cache/deps/onnxruntime/"
  exit 0
fi

# 可选代理参数（支持 http_proxy=url / https_proxy=url，无 = 时沿用环境变量）
for arg in "$@"; do
  case "$arg" in
    http_proxy=*)  export http_proxy="${arg#*=}"  HTTP_PROXY="${arg#*=}"  ;;
    https_proxy=*) export https_proxy="${arg#*=}" HTTPS_PROXY="${arg#*=}" ;;
    no_proxy=*)    export no_proxy="${arg#*=}"    NO_PROXY="${arg#*=}"    ;;
  esac
done

mkdir -p "$ORT_DIR"

# 下载（缓存 tarball，重复构建无需重新下载）
if [[ ! -f "$ORT_TARBALL" ]]; then
  echo "Downloading ONNX Runtime ${ORT_VERSION} (Linux x64) ..."
  if command -v curl &>/dev/null; then
    curl -fL --retry 3 -o "$ORT_TARBALL" "$ORT_URL"
  elif command -v wget &>/dev/null; then
    wget -O "$ORT_TARBALL" "$ORT_URL"
  else
    echo "ERROR: neither curl nor wget found; cannot download ONNX Runtime" >&2
    exit 1
  fi
  echo "ONNX Runtime tarball saved to $ORT_TARBALL"
else
  echo "ONNX Runtime tarball ready: $ORT_TARBALL"
fi

# 解压并安装 include/ + lib/
echo "Extracting ONNX Runtime ..."
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
tar -C "$tmp" -xzf "$ORT_TARBALL"
src="$(find "$tmp" -maxdepth 1 -mindepth 1 -type d | head -1)"
[[ -n "$src" ]] || { echo "ERROR: unexpected tarball layout (no top-level dir)" >&2; exit 1; }
cp -r "$src/include" "$ORT_DIR/"
cp -r "$src/lib"     "$ORT_DIR/"
echo "ONNX Runtime installed to dist/cache/deps/onnxruntime/ (include/ + lib/)"
