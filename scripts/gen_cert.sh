#!/usr/bin/env bash
# 生成自签名证书，供 docutil-server 的 HTTPS 使用。
#   ./scripts/gen_cert.sh [证书输出目录]
#
# 默认输出到 certs/cert.pem + certs/key.pem。
# 生成 10 年有效期的 RSA-2048 自签名证书，CN 设为 localhost（含 SAN）。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
OUT_DIR="${1:-$SCRIPT_DIR/certs}"

CERT_FILE="$OUT_DIR/cert.pem"
KEY_FILE="$OUT_DIR/key.pem"

# 已存在则跳过
if [[ -f "$CERT_FILE" && -f "$KEY_FILE" ]]; then
  echo "自签名证书已存在: $CERT_FILE + $KEY_FILE"
  exit 0
fi

if ! command -v openssl &>/dev/null; then
  echo "ERROR: 需要 openssl 生成自签名证书" >&2
  exit 1
fi

mkdir -p "$OUT_DIR"
echo "生成自签名证书（RSA-2048，10 年有效期）..."
openssl req -x509 -newkey rsa:2048 -sha256 -days 3650 -nodes \
  -keyout "$KEY_FILE" \
  -out "$CERT_FILE" \
  -subj "/C=CN/O=docutil/CN=localhost" \
  -addext "subjectAltName=DNS:localhost,IP:127.0.0.1"

echo "证书生成完毕:"
echo "  $CERT_FILE"
echo "  $KEY_FILE"
