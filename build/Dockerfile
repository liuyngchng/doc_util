# ================================================================
# docutil build image.
#   System packages + Go toolchain only.
#
# Go module cache is mapped from the host at build time via a volume
# (see build_cli.sh), so dependencies are never downloaded inside the image
# and rebuilds stay fast.
#
# go-fitz bundles pre-built MuPDF static libs for ALL platforms
# (linux/windows/darwin/…) so we do NOT need to compile MuPDF.
# ================================================================
FROM ubuntu:24.04 AS base

ARG HTTP_PROXY=""
ARG HTTPS_PROXY=""
ARG NO_PROXY=""
ENV HTTP_PROXY="$HTTP_PROXY" HTTPS_PROXY="$HTTPS_PROXY" NO_PROXY="$NO_PROXY" \
    http_proxy="$HTTP_PROXY" https_proxy="$HTTPS_PROXY" no_proxy="$NO_PROXY"

RUN if [ -n "$HTTP_PROXY" ]; then \
        echo "Acquire::http::Proxy \"$HTTP_PROXY\";" > /etc/apt/apt.conf.d/01proxy; \
    fi; \
    if [ -n "$HTTPS_PROXY" ]; then \
        echo "Acquire::https::Proxy \"$HTTPS_PROXY\";" >> /etc/apt/apt.conf.d/01proxy; \
    fi

RUN apt-get update && apt-get install -y --no-install-recommends \
    # Build essentials
    ca-certificates gcc g++ libc6-dev pkg-config git \
    # MinGW-w64 (Windows cross-compilation)
    gcc-mingw-w64-x86-64 \
    # Fyne: OpenGL rendering
    libgl1-mesa-dev libegl1-mesa-dev libgles2-mesa-dev \
    # Fyne: X11 window system
    libx11-dev libxrandr-dev libxcursor-dev libxinerama-dev libxi-dev \
    libxxf86vm-dev \
    # Fyne: Wayland
    libwayland-dev wayland-protocols libxkbcommon-dev \
    # OCR: OpenCV
    libopencv-dev patchelf \
    && rm -rf /var/lib/apt/lists/*

# ── Go toolchain (from host-cached tarball) ──────────────────────
ARG GO_VERSION=1.24.13
COPY dist/cache/deps/go${GO_VERSION}.linux-amd64.tar.gz /tmp/go.tar.gz
RUN tar -C /usr/local -xzf /tmp/go.tar.gz && rm /tmp/go.tar.gz

ENV PATH="/usr/local/go/bin:${PATH}"
ENV GOTOOLCHAIN=local
ENV GOPROXY=https://goproxy.cn,direct

# ── garble (Go obfuscator) ────────────────────────────────────────
ENV GOBIN=/usr/local/go/bin
RUN go install mvdan.cc/garble@v0.14.2

WORKDIR /workspace