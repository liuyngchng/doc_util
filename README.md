# docutil

将 PDF 逐页渲染为图片，并嵌入 Word 文档的桌面工具。适合在 Word 中像翻页一样逐页阅读 PDF 内容（尤其是扫描件）。

**新功能**：可选 OCR 扩展——启用后同时生成一份纯文字版 `.ocr.docx`，方便搜索和复制文本。

**新功能（v1.1）**：截图版支持**热词红线标注**——用户指定关键词，转换时自动在截图版 DOCX 中用红色下划线标出这些词。有文字层的 PDF 用 MuPDF 定位（快），扫描件和图片文件用 OCR 定位（需 OCR 模型）。

## 运行环境要求

| 模式 | 依赖 |
|------|------|
| 基础版（截图 DOCX） | 无额外依赖，静态二进制 |
| OCR 版（截图 + 文字 DOCX） | `libopencv_core`、`libopencv_imgproc`（系统安装或随发行版附带） + 同目录下的 `libonnxruntime.so` 和 `models/` 模型文件 |

OCR 版启动后，如果 GUI 检测到 `models/` 目录（含 `det/inference.onnx`、`rec/inference.onnx`、`rec/inference.yml`），则出现"生成 OCR 文字版"复选框；如果未检测到，复选框灰掉，行为等同于基础版。

## 技术栈

| 组件 | 选型 | 说明 |
|------|------|------|
| GUI | [Fyne](https://fyne.io/) v2 | Go 原生 GUI 框架 |
| PDF 渲染 | [go-fitz](https://github.com/gen2brain/go-fitz) (MuPDF) | 工业级 PDF 引擎，支持所有 PDF 版本，文字+图片+矢量全渲染 |
| Word 生成 | 手写 OOXML（标准库 `archive/zip` + `encoding/xml`） | 无外部依赖，输出标准 .docx |
| OCR 检测 | ONNX Runtime + OpenCV | 运行 PaddleOCR DB-Net 文本检测模型 |
| OCR 识别 | ONNX Runtime | 运行 PaddleOCR CRNN 文字识别模型，CTC 解码 |

## 编译

### 前置条件

- Docker（编译环境全部容器化，本机无需 Go）
- Go 工具链 tarball：放到 `dist/cache/deps/go1.24.13.linux-amd64.tar.gz`（`build/build_cli.sh` 首次会自动下载）

### 一键编译

```bash
# 默认构建（Linux OCR 版 + Windows 交叉编译）
./build/build_cli.sh
```

产出 `dist/` 下：

| 文件 | 平台 | 说明 |
|------|------|------|
| `docutil` | Linux amd64 | GUI 桌面版（含 OCR 支持） |
| `docutil.exe` | Windows amd64 | GUI 桌面版（交叉编译，含 MuPDF 文本提取，无 OCR） |
| `libonnxruntime.so` | Linux amd64 | ONNX Runtime 运行时（OCR 版附带） |
| `models/` | — | OCR 模型目录（OCR 版附带） |

Server 版通过 `./build/build_server.sh` 构建生产 Docker 镜像 `doc_util:<版本号>`（版本号见 `VERSION` 文件）。

MCP Server 版通过 `./build/build_mcp.sh` 构建生产 Docker 镜像 `doc_util-mcp:<版本号>`。

Linux 构建始终集成 OCR（OpenCV + ONNX Runtime），产出含 .so 库和 models/ 的完整包。
Windows 交叉编译使用 `-tags noocr`（不支持 Windows OCR 交叉编译），但 MuPDF 文本提取始终可用，
因此 Windows 版仍能生成文字版 `.text.docx`（仅扫描件需要 OCR 降级）。

### Windows OCR 版编译

由于 OpenCV C++ ABI 不兼容（MinGW 无法链接 MSVC 编译的 OpenCV DLL），Windows OCR 版**必须在 Windows 上原生编译**，无法在 Docker 中交叉编译。

**编译者需在 Windows 上安装 [MSYS2](https://www.msys2.org/)**（获得 MinGW-w64 工具链 + pacman 包管理）。

安装后打开"MSYS2 UCRT64"终端，执行一次软件包更新：

```bash
pacman -Syu
# 按提示关闭终端，再打开再执行一次（直到无更新）
pacman -Su
```

然后**在 cmd 或 PowerShell 中**（不需要从 MSYS2 终端启动）运行：

```cmd
build\build.bat

# 如果连网需要代理， 则执行如下命令,例如 build\build.bat --proxy http://123.456.789:8080
build\build.bat --proxy http://your_proxoy_host:your_proxy_port
```

产出 `dist\docutil-windows-amd64-ocr.zip`，包含：

| 文件 | 说明 |
|------|------|
| `docutil.exe` | 原生 Windows GUI（含 OCR 支持） |
| `onnxruntime.dll` | ONNX Runtime 运行时 |
| `libopencv_core*.dll` 等 | OpenCV 运行时（自动收集依赖） |
| `models/` | OCR 模型目录 |

**最终用户不需要安装 MSYS2**，解压 zip 双击 `docutil.exe` 即可使用 OCR 功能。

构建默认使用 [garble](https://github.com/burrowers/garble) 对 Windows 交叉编译的模块内代码做符号名和字符串字面量混淆（依赖不变）。Linux OCR 构建不使用 garble（CGO/OCR 与 garble 不兼容）。如果不需要混淆：

```bash
./build/build_cli.sh --no-obfuscate
```

通过代理访问外网：

```bash
./build/build_cli.sh http_proxy=http://proxy:8080 https_proxy=http://proxy:8080
```

### Server 编译

```bash
# 纯 Go 命令行版本（CGO_ENABLED=0，无 OCR，静态链接）
CGO_ENABLED=0 go build -tags noocr -o docutil-cli ./cmd/cli/

# 带 OCR 的命令行版本（CGO_ENABLED=1）
CGO_ENABLED=1 go build -o docutil-cli ./cmd/cli/
```

Server：

```bash
# 构建生产 Docker 镜像（始终带 OCR）
./build/build_server.sh

# 构建 MCP Server Docker 镜像（始终带 OCR）
./build/build_mcp.sh

# 部署（镜像需已构建）
./build/deploy.sh        # server（默认）
./build/deploy.sh mcp    # mcp
```

## 开发环境搭建

开发容器基于 `ubuntu:24.04`，已预装 Go 1.24.13、gcc/g++、OpenCV 4.6、ONNX Runtime 头文件和库。

```bash
# 首次：构建开发镜像
docker build -t docutil_build:latest -f build/Dockerfile .

# 进入开发容器
./build/dev.sh

# 容器内可执行：
go build ./internal/ocr/          # 编译 OCR 包
go build -o dist/docutil .       # 编译 GUI
go vet ./internal/...             # 静态检查
```

### 开发容器内依赖

| 组件 | 版本 | 来源 |
|------|------|------|
| ONNX Runtime | 1.27.1 | `dist/cache/deps/onnxruntime/`（已 vendor 头文件 + libonnxruntime.so） |
| OpenCV | 4.6 | `apt-get install libopencv-dev`（Dockerfile 自动安装） |
| Go | 1.24.13 | 基础镜像预装 |

### 模型准备

OCR 需要 PaddleOCR 的 ONNX 模型。放到可执行文件同级的 `models/` 目录下：

```
models/
├── det/
│   └── inference.onnx       # 文本检测模型（DB-Net）
└── rec/
    ├── inference.onnx        # 文字识别模型（CRNN）
    └── inference.yml         # 字符集配置
```

模型来源于 PaddleOCR 的 ONNX 导出，可从 Android 版 `rd_app` 的 `app/src/main/assets/models/` 目录复制。

## 使用

### GUI

1. 运行 `./docutil`（Linux）或 `docutil.exe`（Windows）
2. 弹出文件选择对话框，选一个或多个 PDF 文件
3. 勾选输出类型（至少选一个，选择会自动记忆，下次打开无需重选）：
   - **生成截图版 (.docx)** — 每页渲染为图片嵌入 Word。勾选后下方出现热词输入框，可填入需要红色下划线标注的关键词（多个词用逗号/分号/换行分隔）。
   - **生成文字版 (.text.docx)** — 提取文字（有文字层的 PDF 直接提取；扫描件需 OCR，仅 Linux 版支持）
4. 点击"开始转换"
5. 同目录下生成对应的 `.docx` / `.text.docx` 文件，用 Word / WPS 打开

### CLI

```bash
# 基础转换（截图版）
./docutil-cli input.pdf

# 文字版
./docutil-cli --mode text input.pdf

# 截图版 + 热词红线标注
./docutil-cli --hotwords "关键词1,关键词2,关键词3" input.pdf
```

### Server

Server 提供三个接口，支持 PDF 和 DOCX 输入：

```bash
# 启动服务
PORT=8080 ./doc_util

# 截图版 DOCX
curl -X POST http://localhost:8080/convert/image \
  -F "file=@input.pdf" \
  -o output.docx

# 截图版 DOCX（带热词标注）
curl -X POST http://localhost:8080/convert/image \
  -F "file=@input.pdf" \
  -F "hotwords=关键词1,关键词2,关键词3" \
  -o output.docx

# 文字版 DOCX
curl -X POST http://localhost:8080/convert/text \
  -F "file=@input.pdf" \
  -o output.text.docx

# 纯文本提取（返回 JSON，含 OCR 置信度）
curl -X POST http://localhost:8080/extract/txt \
  -F "file=@input.pdf"
```

### MCP Server

MCP Server 通过 stdio 的 JSON-RPC 2.0 协议暴露 PDF→DOCX 转换能力，可被
Claude Desktop、Cline 等 MCP 客户端直接调用（让 LLM 直接转换 PDF）。

暴露两个工具：

| 工具 | 说明 |
|------|------|
| `convert_pdf_to_docx_image` | PDF → 截图版 DOCX（每页渲染为图片嵌入 Word） |
| `convert_pdf_to_docx_text` | PDF → 文字版 DOCX（有文字层直接提取，扫描件 OCR） |

每个工具支持两种输入/输出方式（二选一）：

- **输入**：`file_path`（本地路径，要求与客户端共享文件系统）或 `file_data`（base64 编码的 PDF）
- **输出**：`output_path`（写到指定本地路径）或 `file_data`（base64 编码返回，默认）

注册到 Claude Desktop（`claude_desktop_config.json`）：

```json
{
  "mcpServers": {
    "docutil": {
      "command": "/path/to/doc_util-mcp"
    }
  }
}
```

或通过 Docker：

```json
{
  "mcpServers": {
    "docutil": {
      "command": "docker",
      "args": ["run", "-i", "--rm", "doc_util-mcp:1.0.0"]
    }
  }
}
```

## 编译原理

```
ubuntu:24.04
  └─ 安装 gcc + g++ + MinGW-w64 + X11/GL/Wayland 开发头文件
       + libopencv-dev + patchelf + Go toolchain
       └─ go mod download（模块缓存，挂载外部 volume 复用）
            ├─ CGO_ENABLED=1 GOOS=linux   → docutil（ELF，含 OCR）
            │     └─ 链接 libopencv_core + libopencv_imgproc + libonnxruntime
            └─ CGO_ENABLED=1 GOOS=windows
               CC=x86_64-w64-mingw32-gcc  → docutil.exe（PE32+）
               （Windows 版使用 -tags noocr，无 OCR，但含 MuPDF 文本提取）

Windows OCR 版需在 Windows 上通过 build/build.bat 使用 MSYS2 MinGW-w64 原生编译：
  MSYS2 UCRT64 (Windows)
    ├─ pacman 安装 mingw-w64-ucrt-x86_64-go/gcc/opencv
    ├─ 下载 onnxruntime Windows 预编译包 → gendef + dlltool 生成 MinGW 导入库
    ├─ CGO_ENABLED=1 GOOS=windows → docutil.exe（PE32+，含 OCR）
    └─ 收集 DLL + models/ → zip 分发包

MuPDF 静态库由 go-fitz 内置提供（libmupdf_linux_amd64.a / libmupdf_windows_amd64.a），
编译时直接链接进二进制，运行时不需要任何外部 .so / .dll。
```

## 项目结构

```
docutil/
├── main.go                     # Fyne GUI 入口
├── build/
│   ├── Dockerfile              # 编译镜像（ubuntu:24.04 + Go + OpenCV + ONNX Runtime）
│   ├── Dockerfile.server       # Server 生产镜像
│   ├── Dockerfile.mcp          # MCP Server 生产镜像
│   ├── dev.sh                  # 启动开发容器
│   ├── build_cli.sh            # GUI 一键编译（Docker 容器化，Linux OCR + Windows 交叉编译）
│   ├── build.bat               # Windows OCR 版编译（MSYS2 MinGW-w64 原生编译）
│   ├── build_server.sh         # Server 一键编译
│   ├── build_mcp.sh            # MCP Server 一键编译
│   ├── deploy.sh                # 一键构建+部署+跟踪日志
	│   └── collect_dlls.sh         # Windows DLL 收集（被 build.bat 调用）
├── go.mod / go.sum
├── cmd/
│   ├── cli/main.go             # 纯 Go 命令行版本
│   ├── server/main.go          # HTTP 服务端
│   └── mcp/main.go             # MCP 服务端（stdio JSON-RPC）
├── internal/
│   ├── pdf/
│   │   ├── convert.go          # PDF → DOCX（图片/文字模式，含 OCR 集成）
│   │   └── extract.go          # PDF → 纯文本提取
│   ├── doc/
│   │   ├── convert.go          # 图片 → DOCX、热词标注、路径生成
│   │   ├── extract.go          # DOCX/.doc → 纯文本提取（含置信度）、.doc→DOCX
│   │   ├── docx.go             # 手写 OOXML 打包（截图版）
│   │   ├── docx_text.go        # 手写 OOXML 打包（OCR 文字版）
│   │   └── ocr.go              # OCR 引擎、印章检测、文本清洗
│   └── ocr/
│       ├── types.go            # 基础类型（OCRBox / TextLine / PageText）
│       ├── model_config.go     # 解析 inference.yml 字符表 + CheckModels()
│       ├── engine.go           # OCR 引擎（检测→识别→文本，需 CGO）
│       ├── onnx_api.go         # CGO 绑定 ONNX Runtime C API
│       ├── ort_bridge.c        # ONNX Runtime C 调用封装
│       ├── opencv.go           # CGO 绑定 OpenCV
│       ├── opencv_bridge.cpp   # OpenCV C++→C 桥接函数
│       ├── preprocess_det.go   # 检测预处理（resize→normalize）
│       ├── preprocess_rec.go   # 识别预处理（crop→resize→normalize）
│       ├── postprocess_db.go   # DB 后处理（threshold→连通域→minAreaRect→unclip）
│       ├── postprocess_ctc.go  # CTC 贪心解码
│       ├── box_sort.go         # 阅读顺序排列
│       └── stub.go             # 无 CGO 时的桩实现
└── dist/cache/
    ├── deps/
    │   ├── go1.24.13.linux-amd64.tar.gz   # Go toolchain（build_cli.sh 缓存）
    │   └── onnxruntime/                    # 已 vendor
    │       ├── include/onnxruntime_c_api.h
    │       └── lib/libonnxruntime.so
    ├── gocache/                # Go 编译缓存（挂载 volume，加速重复编译）
    └── gomodcache/             # Go 模块缓存（挂载 volume）
```

## 部署

### GUI

直接将 `dist/` 下的 tar 包解压到目标 Linux 机器即可运行：

```bash
tar -xf docutil-linux-amd64.tar
cd docutil-linux-amd64/
./docutil
```

目标机器需要：

1. **系统库**：`libopencv_core.so.406` + `libopencv_imgproc.so.406`
   ```bash
   sudo apt-get install -y libopencv-core406 libopencv-imgproc406
   ```

2. **ONNX Runtime**：已内置在 tar 包中（`libonnxruntime.so`），二进制已设置 `$ORIGIN` rpath，自动查找。

3. **OCR 模型**：已内置在 tar 包中（`models/`）。

### Server

直接运行 Docker 镜像：

```bash
docker run -p 8080:8080 doc_util:1.0.0
```

### MCP Server

MCP Server 通过 stdio 通信，作为子进程被 MCP 客户端拉起，无需监听端口。

两种部署方式：

1. **直接运行二进制**（客户端与 MCP Server 共享文件系统，可用 `file_path` 传入 PDF）：

   ```json
   {
     "mcpServers": {
       "docutil": {
         "command": "/path/to/doc_util-mcp"
       }
     }
   }
   ```

2. **通过 Docker 运行**（注意：`-i` 必须保留以维持 stdin，且需 `-v` 挂载目录才能用 `file_path`；否则只能用 `file_data` base64 方式）：

   ```json
   {
     "mcpServers": {
       "docutil": {
         "command": "docker",
         "args": ["run", "-i", "--rm", "-v", "/tmp:/tmp", "doc_util-mcp:1.0.0"]
       }
     }
   }
   ```

环境变量：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `LOG_LEVEL` | `info` | 日志级别：`debug` / `info` / `warn` / `error`（日志写 stderr，不影响 stdout 上的 JSON-RPC） |

## License

MIT