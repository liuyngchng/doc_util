// Command mcp exposes PDF → DOCX conversion as a Model Context Protocol (MCP)
// server over stdio (JSON-RPC 2.0). It can be registered with any MCP client
// (Claude Desktop, Cline, etc.) so an LLM can convert PDFs to Word directly.
//
// Build (Docker, with OCR):
//
//	./build/build_mcp.sh
//
// Register in an MCP client (Claude Desktop's claude_desktop_config.json):
//
//	{
//	  "mcpServers": {
//	    "docutil": {
//	      "command": "/path/to/doc_util-mcp"
//	    }
//	  }
//	}
//
// Two tools are exposed:
//
//	convert_pdf_to_docx_image → screenshot DOCX (every page rendered as an image)
//	convert_pdf_to_docx_text  → text-layer DOCX (extract text for native PDFs, OCR for scans)
//
// Input may be a local file path or base64-encoded PDF bytes; output is either
// a local file path or base64-encoded DOCX bytes (see each tool's inputSchema).
package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"docutil/internal/doc"
	"docutil/internal/pdf"
	"docutil/internal/slogutil"
)

// serverVersion is reported in the initialize response's serverInfo.
const serverVersion = "1.0.0"

// mcpProtocolVersion is the MCP protocol revision this server implements.
// "2024-11-05" is the most widely supported revision across current clients.
const mcpProtocolVersion = "2024-11-05"

// maxPDFBytes caps the decoded PDF size (matches the HTTP server's 64 MB limit).
const maxPDFBytes = 64 << 20

func main() {
	slogger := slog.New(slogutil.NewHandler(os.Stderr, &slog.HandlerOptions{
		Level: slogLevel(),
	}))
	slog.SetDefault(slogger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	slog.Info("mcp server starting", "protocolVersion", mcpProtocolVersion, "version", serverVersion)

	// Serve on stdin/stdout. Logging goes to stderr so it never pollutes the
	// JSON-RPC stream on stdout.
	if err := serve(ctx, os.Stdin, os.Stdout); err != nil && !errors.Is(err, io.EOF) {
		slog.Error("mcp server fatal", "error", err)
		os.Exit(1)
	}
	slog.Info("mcp server stopped")
}

func slogLevel() slog.Level {
	switch os.Getenv("LOG_LEVEL") {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// ── JSON-RPC 2.0 wire types ─────────────────────────────────────────────────

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// ── Serve loop ──────────────────────────────────────────────────────────────

// serve reads newline-delimited JSON-RPC messages from in and writes responses
// to out. MCP stdio transport is one JSON-RPC message per line; a single line
// may carry a large base64 payload, so the read buffer is sized generously.
func serve(ctx context.Context, in io.Reader, out io.Writer) error {
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 256*1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}

		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			slog.Warn("malformed JSON-RPC message", "error", err)
			writeResponse(enc, rpcResponse{
				JSONRPC: "2.0",
				Error:   &rpcError{Code: -32700, Message: "parse error: " + err.Error()},
			})
			continue
		}

		resp, isNotification := dispatch(ctx, req)
		if isNotification {
			continue
		}
		resp.JSONRPC = "2.0"
		writeResponse(enc, resp)
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	return nil
}

func writeResponse(enc *json.Encoder, resp rpcResponse) {
	if err := enc.Encode(resp); err != nil {
		slog.Warn("write response failed", "error", err)
	}
}

// dispatch routes a request to its handler. It returns isNotification=true for
// notifications (which must not produce a response).
func dispatch(ctx context.Context, req rpcRequest) (rpcResponse, bool) {
	// Notifications have no id.
	isNotification := len(req.ID) == 0 || string(req.ID) == "null"

	switch req.Method {
	case "initialize":
		return handleInitialize(req), false
	case "notifications/initialized":
		return rpcResponse{}, true
	case "ping":
		return rpcResponse{ID: req.ID, Result: struct{}{}}, false
	case "tools/list":
		return rpcResponse{ID: req.ID, Result: listTools()}, false
	case "tools/call":
		return handleToolsCall(ctx, req), false
	default:
		if isNotification {
			// Unknown notification — ignore silently.
			return rpcResponse{}, true
		}
		return rpcResponse{
			ID:    req.ID,
			Error: &rpcError{Code: -32601, Message: "method not found: " + req.Method},
		}, false
	}
}

// ── initialize ──────────────────────────────────────────────────────────────

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type capabilities struct {
	Tools struct {
		ListChanged bool `json:"listChanged"`
	} `json:"tools"`
}

type initResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    capabilities `json:"capabilities"`
	ServerInfo      serverInfo   `json:"serverInfo"`
}

func handleInitialize(req rpcRequest) rpcResponse {
	// Extract the client's requested protocol version for logging.
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(req.Params, &params)
	slog.Info("initialize", "clientProtocolVersion", params.ProtocolVersion)

	cap := capabilities{}
	cap.Tools.ListChanged = false

	return rpcResponse{
		ID: req.ID,
		Result: initResult{
			ProtocolVersion: mcpProtocolVersion,
			Capabilities:    cap,
			ServerInfo:      serverInfo{Name: "docutil", Version: serverVersion},
		},
	}
}

// ── tools/list ──────────────────────────────────────────────────────────────

type inputSchema struct {
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
	Required   []string       `json:"required,omitempty"`
}

type toolDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema inputSchema `json:"inputSchema"`
}

type toolsListResult struct {
	Tools []toolDef `json:"tools"`
}

// fileArgsSchema is the shared input schema for both conversion tools: the PDF
// may be supplied as a local path or as base64 bytes (exactly one required),
// and the DOCX may be written to a local path or returned as base64 bytes.
func fileArgsSchema() inputSchema {
	props := map[string]any{
		"file_path": map[string]any{
			"type":        "string",
			"description": "Absolute path to the input PDF file on the local filesystem. Use this when the MCP server shares a filesystem with the client.",
		},
		"file_data": map[string]any{
			"type":        "string",
			"description": "Base64-encoded PDF file contents. Use this when the PDF is not on a local filesystem accessible to the server.",
		},
		"output_path": map[string]any{
			"type":        "string",
			"description": "Optional absolute path where the generated DOCX should be written. If omitted, the DOCX is returned as base64 in file_data.",
		},
		"hotwords": map[string]any{
			"type":        "string",
			"description": "Optional, image mode only. A list of hotwords to annotate with a red underline, separated by commas, semicolons, or newlines.",
		},
	}
	return inputSchema{Type: "object", Properties: props}
}

func listTools() toolsListResult {
	return toolsListResult{
		Tools: []toolDef{
			{
				Name:        "convert_pdf_to_docx_image",
				Description: "Convert a PDF to a DOCX where each page is rendered as an embedded image (screenshot style). Works for any PDF including pure scans. Provide either file_path or file_data.",
				InputSchema: fileArgsSchema(),
			},
			{
				Name:        "convert_pdf_to_docx_text",
				Description: "Convert a PDF to a DOCX with an editable text layer. Extracts the native text layer when present, and falls back to OCR for scanned pages (requires the OCR models). Provide either file_path or file_data.",
				InputSchema: fileArgsSchema(),
			},
		},
	}
}

// ── tools/call ──────────────────────────────────────────────────────────────

type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolsCallResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError"`
}

type convertArgs struct {
	FilePath   string `json:"file_path"`
	FileData   string `json:"file_data"`
	OutputPath string `json:"output_path"`
	Hotwords   string `json:"hotwords"`
}

func handleToolsCall(ctx context.Context, req rpcRequest) rpcResponse {
	var params toolsCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return toolError(req.ID, "invalid tools/call params: "+err.Error())
	}

	var mode doc.ConvertMode
	switch params.Name {
	case "convert_pdf_to_docx_image":
		mode = doc.ModeImage
	case "convert_pdf_to_docx_text":
		mode = doc.ModeText
	default:
		return toolError(req.ID, "unknown tool: "+params.Name)
	}

	var args convertArgs
	if err := json.Unmarshal(params.Arguments, &args); err != nil {
		return toolError(req.ID, "invalid arguments: "+err.Error())
	}

	start := time.Now()
	slog.Info("conversion requested", "tool", params.Name, "mode", mode)

	resultJSON, err := convert(ctx, mode, args)
	if err != nil {
		slog.Error("conversion failed", "tool", params.Name, "error", err, "duration_ms", time.Since(start).Milliseconds())
		return toolError(req.ID, err.Error())
	}

	slog.Info("conversion done", "tool", params.Name, "duration_ms", time.Since(start).Milliseconds())
	return rpcResponse{
		ID: req.ID,
		Result: toolsCallResult{
			Content: []contentBlock{{Type: "text", Text: resultJSON}},
			IsError: false,
		},
	}
}

// toolError wraps an error into a tools/call result with isError=true. The MCP
// client surfaces the text to the LLM so it can recover or report the failure.
func toolError(id json.RawMessage, msg string) rpcResponse {
	return rpcResponse{
		ID: id,
		Result: toolsCallResult{
			Content: []contentBlock{{Type: "text", Text: "conversion failed: " + msg}},
			IsError: true,
		},
	}
}

// ── Conversion ──────────────────────────────────────────────────────────────

// convert resolves the input PDF, runs the conversion, and returns a JSON
// payload describing the result (either an output_path or base64 file_data).
func convert(ctx context.Context, mode doc.ConvertMode, args convertArgs) (string, error) {
	// Resolve input PDF to a temp file.
	pdfTmp, err := os.CreateTemp("", "docutil-*.pdf")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(pdfTmp.Name())
	pdfName := pdfTmp.Name()

	switch {
	case args.FilePath != "" && args.FileData != "":
		return "", errors.New("provide exactly one of file_path or file_data, not both")
	case args.FilePath != "":
		if err := copyFile(args.FilePath, pdfTmp); err != nil {
			return "", err
		}
		// Preserve the original filename for a friendlier output name.
		pdfName = args.FilePath
	case args.FileData != "":
		decoded, err := base64.StdEncoding.DecodeString(args.FileData)
		if err != nil {
			return "", fmt.Errorf("decode file_data: %w", err)
		}
		if len(decoded) > maxPDFBytes {
			return "", fmt.Errorf("PDF too large: %d bytes (max %d)", len(decoded), maxPDFBytes)
		}
		if _, err := pdfTmp.Write(decoded); err != nil {
			pdfTmp.Close()
			return "", fmt.Errorf("write temp file: %w", err)
		}
		pdfTmp.Close()
		// No original filename available for base64 input — use a friendly
		// default so the output name isn't the temp file name.
		pdfName = "document.pdf"
	default:
		return "", errors.New("provide exactly one of file_path or file_data")
	}

	// Convert.
	hotwords := doc.ParseHotwords(args.Hotwords)
	docxPath, err := pdf.ConvertContext(ctx, pdfTmp.Name(), mode, hotwords, func(pct float64) {})
	if err != nil {
		return "", fmt.Errorf("convert: %w", err)
	}
	defer os.Remove(docxPath)

	docxData, err := os.ReadFile(docxPath)
	if err != nil {
		return "", fmt.Errorf("read result: %w", err)
	}

	outName := docxFilename(pdfName, mode)

	// Emit result.
	result := map[string]any{
		"ok":         true,
		"file_name":  outName,
		"size_bytes": len(docxData),
		"mode":       modeLabel(mode),
	}

	if args.OutputPath != "" {
		if err := os.WriteFile(args.OutputPath, docxData, 0o644); err != nil {
			return "", fmt.Errorf("write output: %w", err)
		}
		result["output_path"] = args.OutputPath
	} else {
		result["file_data"] = base64.StdEncoding.EncodeToString(docxData)
	}

	b, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(b), nil
}

// copyFile copies src to an already-open temp file, enforcing the size cap.
func copyFile(src string, dst *os.File) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open file_path: %w", err)
	}
	defer in.Close()

	n, err := io.Copy(dst, io.LimitReader(in, maxPDFBytes+1))
	if err != nil {
		return fmt.Errorf("read file_path: %w", err)
	}
	if n > maxPDFBytes {
		return fmt.Errorf("PDF too large: %d bytes (max %d)", n, maxPDFBytes)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	return nil
}

func docxFilename(pdfName string, mode doc.ConvertMode) string {
	ext := filepath.Ext(pdfName)
	base := pdfName[:len(pdfName)-len(ext)]
	if mode == doc.ModeText {
		return base + ".text.docx"
	}
	return base + ".docx"
}

func modeLabel(mode doc.ConvertMode) string {
	if mode == doc.ModeText {
		return "text"
	}
	return "image"
}
