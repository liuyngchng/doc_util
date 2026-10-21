# docutil-server API 文档

## 概述

HTTP 服务，接收 PDF / DOCX / .doc 文件，返回转换结果。提供三种能力：

| 端点 | 模式 | 产出 | 输入格式 |
|------|------|------|----------|
| `/convert/image` | 截图版 | `.docx` | PDF |
| `/convert/text` | 文字版 | `.text.docx` | PDF |
| `/extract/txt` | 纯文本 | JSON | PDF / DOCX / .doc |

---

## 端点

### 1. 健康检查

```
GET /health
```

**响应**

```
200 OK
Content-Type: application/json

{"status":"ok"}
```

---

### 2. 截图版转换

```
POST /convert/image
Content-Type: multipart/form-data
```

每页渲染为 JPEG 图片嵌入 DOCX，适合所有 PDF（包括纯扫描件）。支持对指定的热词（hotwords）自动标注红色下划线。

**请求**

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `file` | file | 是 | 要转换的 PDF 文件（仅接受 `%PDF` magic bytes），最大 64 MB |
| `hotwords` | string | 否 | 要标注的热词列表，多个热词用逗号、分号或换行分隔（大小写不敏感） |

**成功响应**

```
200 OK
Content-Type: application/vnd.openxmlformats-officedocument.wordprocessingml.document
Content-Disposition: attachment; filename="xxx.docx"

<DOCX 二进制流>
```

#### 热词标注（hotwords）

当传入 `hotwords` 参数时，截图版转换会在输出 DOCX 的每一页图片上，自动搜索这些关键词，并在匹配位置绘制**红色下划线**。

**PDF 文件**：
- **有文字层**：直接利用 PDF 内嵌文字的空间坐标定位，速度快、位置精确
- **无文字层（扫描件）**：自动运行 OCR 识别文字并获取文本框坐标来定位，需要 OCR 模型

**热词分隔格式示例**：

```
# 逗号分隔
hotwords=关键词1,关键词2,关键词3

# 分号分隔
hotwords=关键词1;关键词2

# 混合分隔
hotwords=关键词1,关键词2;关键词3
```

**注意事项**：
- 匹配是大小写不敏感的（`Hello` 和 `hello` 等效）
- 中文热词支持良好
- 热词跨 span / 跨行时可能无法完整匹配，建议热词不要太长
- 标注是在渲染后的图片上直接画线，不会修改 DOCX 的文字层

---

### 3. 文字版转换

```
POST /convert/text
Content-Type: multipart/form-data
```

优先使用 MuPDF 提取 PDF 内嵌文字层；对纯扫描件（无文字层）回退到 OCR 识别。

**请求**

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `file` | file | 是 | 要转换的 PDF 文件（仅接受 `%PDF` magic bytes），最大 64 MB |

**成功响应**

```
200 OK
Content-Type: application/vnd.openxmlformats-officedocument.wordprocessingml.document
Content-Disposition: attachment; filename="xxx.text.docx"

<DOCX 二进制流>
```

---

### 4. 纯文本提取

```
POST /extract/txt
Content-Type: multipart/form-data
```

接收 PDF、DOCX 或旧版 Word `.doc` 文件，返回其中全部文本及 OCR 置信度（JSON）。文件类型根据 magic bytes 自动识别，不依赖文件名。图片中的文字会通过 OCR 提取。

#### 输入格式支持

| 格式 | 扩展名 | 检测方式 | 说明 |
|------|--------|----------|------|
| PDF | `.pdf` | `%PDF` magic | MuPDF 文字层提取 + 扫描页 OCR 渲染 |
| DOCX | `.docx` | `PK` ZIP magic + `word/document.xml` | 解析正文文本 + 内嵌图片 OCR |
| .doc | `.doc` | OLE2 Compound Document magic (`D0 CF 11 E0`) | LibreOffice headless → DOCX → 提取文本 |

其他格式（如 `.odt`、`.xls` 等）不会被识别，直接走 PDF 分支，MuPDF 打开失败返回错误。

**请求**

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `file` | file | 是 | 要提取的 PDF / DOCX / .doc 文件，最大 64 MB |

**成功响应**

```
200 OK
Content-Type: application/json; charset=utf-8

{
  "text": "提取的纯文本内容...",
  "ocr_confidence": 0.95,
  "has_seal": true,
  "seal_text": ["北京XX科技有限公司", "财务专用章"]
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `text` | string | 提取的全部文本（多页以换行分隔） |
| `ocr_confidence` | float64 | 文档级加权平均置信度，0.0–1.0 |
| `has_seal` | bool | 文档是否检测到印章（任意页面检测到即为 true） |
| `seal_text` | []string | 印章区域 OCR 识别出的文字（已清洗去除识别 artifacts）；需要部署 OCR 模型 |

#### 印章检测说明

- 使用 PP-OCRv4 印章检测模型（`models/seal_det/inference.onnx`），检测页面中的圆形/椭圆形印章区域，然后对印章区域做 OCR 提取文字。
- **PDF**：所有页面都做印章检测（印章是图形元素，MuPDF 文字提取无法获取）。有文字层的页面也会渲染为图片来检测印章。
- **DOCX / .doc**：仅对内嵌图片做印章检测。
- 若 OCR 模型或印章检测模型未部署，`seal_text` 返回空数组 `[]`、`has_seal` 为 `false`。

#### 置信度说明

- **1.0**：纯文本提取（PDF 文字层、DOCX 正文文本），无 OCR 参与。
- **0.0–1.0**：部分或全部内容来自 OCR，值越低说明识别越不可靠。
- 计算方式：以每个字符数为权重，对各行置信度做加权平均。OCR 行的置信度为模型返回的行级置信度；原生文本行固定为 1.0。

#### 行为说明

- **PDF**：优先用 MuPDF 提取文字层；纯扫描页（无文字层）渲染为图片后 OCR。**所有页面**（无论有无文字层）都会渲染为图片做印章检测。
- **DOCX**：解析正文文本，并对内嵌图片逐张 OCR，按文档顺序合并返回。
- **.doc**：先调用 LibreOffice headless（`soffice --headless --convert-to docx`）转为 DOCX，再按 DOCX 流程提取；单次转换超时 60s。需要 Docker 镜像安装 `libreoffice-writer-nogui`。

#### 已知限制

- **PDF 内嵌图片**：go-fitz 不暴露「提取页内嵌图片」的 API，因此「既有文字层又嵌了图片」的页面，只提取文字层，页内图片不会被单独 OCR；纯图片页（无文字层）会整页渲染后 OCR。
- **DOCX**：仅处理正文 `word/document.xml` 与 `word/media/` 内嵌图片，不含页眉、页脚、脚注、文本框；无法解码的图片格式（如 EMF/WMF）会被跳过。
- **.doc**：依赖 LibreOffice 转换质量，复杂排版（表格、多栏、嵌入对象）可能有差异。LibreOffice 未安装时返回 500 错误。
- **印章检测**：仅检测圆形/椭圆形的印章区域并提取其文字，不判断印章类型（如「公章」「合同章」「财务章」）；印章文字识别依赖 OCR 模型，识别精度受印章颜色（红章在灰度/彩色渲染下的对比度）影响。

---

### 错误响应

| 状态码 | 说明 |
|--------|------|
| 400 | 请求格式错误（缺少 file 字段、非 multipart、文件格式不支持等） |
| 413 | 文件超过 64 MB |
| 429 | 并发转换数超过上限（`MAX_CONCURRENT_CONVERSIONS`，默认 4） |
| 500 | 转换失败或内部错误 |

---

## 配置

| 环境变量 | 默认值 | 说明 |
|----------|--------|------|
| `PORT` | `8080` | 监听端口 |
| `LOG_LEVEL` | `info` | 日志级别：`debug` / `info` / `warn` / `error` |
| `LOG_DIR` | (空) | 日志输出目录（不设置则只写 stderr） |
| `MAX_CONCURRENT_CONVERSIONS` | `4` | 并发转换上限（超限返回 429） |
| `TLS_CERT_FILE` | (空) | TLS 证书路径（与 `TLS_KEY_FILE` 同时设置时启用 HTTPS） |
| `TLS_KEY_FILE` | (空) | TLS 私钥路径 |

## 示例

### 健康检查

```bash
curl -s -k --noproxy "*" https://localhost:8443/health -w'\n'
```

### 截图版转换

```bash
curl -X POST -k --noproxy "*" https://localhost:8443/convert/image \
  -F "file=@input.pdf" \
  -o output.docx -w'\n'
```

### 截图版转换（带热词标注）

```bash
curl -X POST -k --noproxy "*" https://localhost:8443/convert/image \
  -F "file=@input.pdf" \
  -F "hotwords=关键词1,关键词2,关键词3" \
  -o output.docx -w'\n'
```

### 文字版转换

```bash
curl -X POST -k --noproxy "*" https://localhost:8443/convert/text \
  -F "file=@input.pdf" \
  -o output.text.docx -w'\n'
```

### 纯文本提取

```bash
# PDF
curl -X POST -k --noproxy "*" https://localhost:8443/extract/txt \
  -F "file=@input.pdf" -w'\n'

# DOCX
curl -X POST -k --noproxy "*" https://localhost:8443/extract/txt \
  -F "file=@input.docx" -w'\n'

# .doc（旧版 Word）
curl -X POST -k --noproxy "*" https://localhost:8443/extract/txt \
  -F "file=@document.doc" -w'\n'
```

### 显示上传进度

```bash
curl -X POST -k https://localhost:8443/convert/image \
  -F "file=@input.pdf" \
  -o output.docx \
  --progress-bar -w'\n'
```

### 验证结果

```bash
# 检查返回的文件类型（DOCX 本质是 ZIP）
file output.docx
# 输出: output.docx: Microsoft Word 2007+
```

## 限制

- 上传文件最大 64 MB
- 不支持加密 PDF
- 读写超时：请求 60 秒，响应 300 秒
- 并发转换上限默认 4（可通过 `MAX_CONCURRENT_CONVERSIONS` 调整）