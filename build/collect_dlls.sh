#!/usr/bin/env bash
set -e
# 脚本位于 build/ 目录，dist/ 与 dist/cache/ 在其上级项目根目录下
cd "$(dirname "$0")/.."
export PATH=/ucrt64/bin:$PATH

exe_file=dist/docutil.exe
pkg_dir=dist/docutil-windows-amd64

echo "  Copying DLLs..."

# Collect ucrt64 DLLs
ldd "$exe_file" 2>/dev/null | grep '/ucrt64/bin/' | awk '{print $3}' | sort -u | while read dll; do
    echo "    $dll"
    cp "$dll" "$pkg_dir/"
done

# onnxruntime DLL
ort_bin='dist/cache/deps/onnxruntime/win-x64/bin/onnxruntime.dll'
if [ -f "$ort_bin" ]; then
    echo "    $ort_bin"
    cp "$ort_bin" "$pkg_dir/"
fi

echo "  DLLs copied."
