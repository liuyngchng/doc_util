# docutil-server API 文档

## 概述

HTTP 服务，接收 PDF / DOCX 文件，返回转换结果。提供三种能力：

| 端点 | 模式 | 产出 | 说明 |
|------|------|------|------|
| `/convert/image` | 截图版 | `.docx` | 每页渲染为图片嵌入 Word，始终可用；支持热词红色下划线标注 |
| `/convert/text` | 文字版 | `.text.docx` | 提取文字（有文字层的 PDF 直接提取，扫描件 OCR），需要 OCR 模型 |
| `/extract/txt` | 纯文本 | JSON | 提取 PDF / DOCX 的全部文本及 OCR 置信度，供其他服务调用 |

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
| `file` | file | 是 | 要转换的 PDF 或图片文件，最大 64 MB |
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

**图片文件**（JPG/PNG/BMP/GIF/TIFF/WebP）：
- **必须依赖 OCR 模型**来定位文字位置
- 若无 OCR 模型，热词标注不生效，但转换仍正常完成

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
| `file` | file | 是 | 要转换的 PDF 文件，最大 64 MB |

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

接收 PDF 或 DOCX 文件，返回其中全部文本及 OCR 置信度（JSON）。文件类型根据内容自动识别，不依赖文件名。图片中的文字会通过 OCR 提取。

**请求**

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `file` | file | 是 | 要提取的 PDF 或 DOCX 文件，最大 64 MB |

**成功响应**

```
200 OK
Content-Type: application/json; charset=utf-8

{
  "text": "提取的纯文本内容...",
  "ocr_confidence": 0.95
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `text` | string | 提取的全部文本（多页以换行分隔） |
| `ocr_confidence` | float64 | 文档级加权平均置信度，0.0–1.0 |

#### 置信度说明

- **1.0**：纯文本提取（PDF 文字层、DOCX 正文文本），无 OCR 参与。
- **0.0–1.0**：部分或全部内容来自 OCR，值越低说明识别越不可靠。
- 计算方式：以每个字符数为权重，对各行置信度做加权平均。OCR 行的置信度为模型返回的行级置信度；原生文本行固定为 1.0。

#### 行为说明

- **PDF**：优先用 MuPDF 提取文字层；纯扫描页（无文字层）渲染为图片后 OCR。
- **DOCX**：解析正文文本，并对内嵌图片逐张 OCR，按文档顺序合并返回。

#### 已知限制

- **PDF 内嵌图片**：go-fitz 不暴露「提取页内嵌图片」的 API，因此「既有文字层又嵌了图片」的页面，只提取文字层，页内图片不会被单独 OCR；纯图片页（无文字层）会整页渲染后 OCR。
- **DOCX**：仅处理正文 `word/document.xml` 与 `word/media/` 内嵌图片，不含页眉、页脚、脚注、文本框；无法解码的图片格式（如 EMF/WMF）会被跳过。

---

### 错误响应

| 状态码 | 说明 |
|--------|------|
| 400 | 请求格式错误（缺少 file 字段、非 multipart 等） |
| 413 | 文件超过 64 MB |
| 500 | 转换失败或内部错误 |

---

## 配置

| 环境变量 | 默认值 | 说明 |
|----------|--------|------|
| `PORT` | `8080` | 监听端口 |
| `LOG_LEVEL` | `info` | 日志级别：`debug` / `info` / `warn` / `error` |

## 示例

### 健康检查

```bash
curl -s --noproxy "*" http://localhost:8080/health
```

### 截图版转换

```bash
curl -X POST --noproxy "*" http://localhost:8080/convert/image \
  -F "file=@input.pdf" \
  -o output.docx
```

### 截图版转换（带热词标注）

```bash
curl -X POST --noproxy "*" http://localhost:8080/convert/image \
  -F "file=@input.pdf" \
  -F "hotwords=关键词1,关键词2,关键词3" \
  -o output.docx
```

### 文字版转换

```bash
curl -X POST --noproxy "*" http://localhost:8080/convert/text \
  -F "file=@input.pdf" \
  -o output.text.docx
```

### 纯文本提取

```bash
# PDF
curl -X POST --noproxy "*" http://localhost:8080/extract/txt \
  -F "file=@input.pdf"

# DOCX
curl -X POST --noproxy "*" http://localhost:8080/extract/txt \
  -F "file=@input.docx"
```



### 显示上传进度

```bash
curl -X POST http://localhost:8080/convert/image \
  -F "file=@input.pdf" \
  -o output.docx \
  --progress-bar
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