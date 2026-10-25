# CLAUDE.md

本文件为 Claude Code 提供项目上下文。完整文档见 [README.md](README.md)。

## 编译环境（重要）

**宿主机上没有 Go / OpenCV / ONNX Runtime 编译环境**，直接在宿主机运行
`go build` / `go vet` / `go test` 会失败（报 `opencv4.pc` not found、CGO 类型
`undefined: ORTSession` 等错误）。

编译、静态检查、测试都必须在 Docker 容器内进行：

```bash
# 一键编译（GUI：./build/build_cli.sh，Server：./build/build_server.sh）
./build/build_cli.sh     # Linux OCR 版 + Windows 交叉编译

# 进入开发容器，在容器内执行 go build / go vet / go test
./build/dev.sh
# 容器内：
#   go build ./internal/ocr/
#   go vet ./internal/...
```

- OCR 版依赖 CGO + OpenCV + ONNX Runtime，编译脚本会自动构建/使用
  `docutil_build:latest` 镜像（`build/Dockerfile`）。
- 若只想验证纯 Go 逻辑（不含 CGO/OCR），可用 `CGO_ENABLED=0 go build -tags noocr ...`，
  但 `internal/ocr` 包本身仍需要容器环境才能完整编译。

## 代码结构

- `internal/ocr/` — PaddleOCR 推理（检测 DB-Net + 识别 CRNN，CTC 解码），全部 CGO。
- `internal/pdf/` — PDF 特定逻辑（go-fitz），调用 `internal/doc` 做 DOCX 输出和文本提取
- `internal/doc/` — 文档格式转换（DOCX builder、DOCX/.doc 文本提取、.doc→DOCX、图片→DOCX、OCR 工具函数、共享类型）；`ocr.go` 里的 `cleanOCRLine`
  负责清洗 OCR 文本（去掉 `'` 分隔符、CJK 字符之间的 `-`/空格等 artifacts）。
- `models/rec/inference.yml` 的 `PostProcess.character_dict` 是识别模型的字符表
  （首个字符 `'` 是 PP-OCR 的占位/分隔符）。
