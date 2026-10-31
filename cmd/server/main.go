// Command server is a headless HTTP server that converts PDF to DOCX.
//
// Build (Docker, with OCR):
//
//	./build_server.sh
//
// The server exposes four endpoints:
//
//	POST /convert/image  → screenshot DOCX (always works)
//	POST /convert/text   → text-layer DOCX (extract text for native PDFs, OCR for scans)
//	POST /convert/doc    → .doc to .docx conversion (via LibreOffice)
//	POST /extract/txt    → plain text string (PDF or DOCX input; OCR for image-only content)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"docutil/internal/doc"
	"docutil/internal/ocr"
	"docutil/internal/pdf"
	"docutil/internal/slogutil"
)

func main() {
	// Default handler writes to stderr so docker logs works.
	logWriters := []io.Writer{os.Stderr}

	// If LOG_DIR is set, also write to a rotating daily log file.
	if logDir := os.Getenv("LOG_DIR"); logDir != "" {
		if err := os.MkdirAll(logDir, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "WARN: cannot create LOG_DIR %s: %v\n", logDir, err)
		} else {
			logFile := filepath.Join(logDir, "server.log")
			f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				fmt.Fprintf(os.Stderr, "WARN: cannot open log file %s: %v\n", logFile, err)
			} else {
				logWriters = append(logWriters, f)
				fmt.Fprintf(os.Stderr, "Logging to %s\n", logFile)
			}
		}
	}

	slogger := slog.New(slogutil.NewHandler(io.MultiWriter(logWriters...), &slog.HandlerOptions{
		Level: slogLevel(),
	}))
	slog.SetDefault(slogger)

	// Verify all required OCR models are present before serving. The server
	// always integrates OCR (text conversion, extraction, seal detection), so
	// a missing model is a fatal startup error. Models are still lazily
	// loaded at runtime.
	baseDir, _ := os.Getwd()
	if exe, err := os.Executable(); err == nil {
		baseDir = filepath.Dir(exe)
	}
	if err := ocr.CheckAllModels(baseDir); err != nil {
		slog.Error("required OCR models not found", "error", err)
		os.Exit(1)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// Enforce a cap on concurrent OCR-bound conversions (default 4).
	maxConcurrent := maxConcurrentConversions()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /convert/image", limit(maxConcurrent, handleConvertImage))
	mux.HandleFunc("POST /convert/text", limit(maxConcurrent, handleConvertText))
	mux.HandleFunc("POST /convert/doc", limit(maxConcurrent, handleConvertDoc))
	mux.HandleFunc("POST /extract/txt", limit(maxConcurrent, handleExtractTxt))
	mux.HandleFunc("GET /health", handleHealth)

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      withLogging(mux),
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 300 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown on SIGTERM / SIGINT.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		certFile := os.Getenv("TLS_CERT_FILE")
		keyFile := os.Getenv("TLS_KEY_FILE")
		tlsEnabled := false
		if certFile != "" && keyFile != "" {
			if _, err := os.Stat(certFile); err != nil {
				slog.Warn("TLS_CERT_FILE set but not found, falling back to HTTP", "file", certFile, "error", err)
			} else if _, err := os.Stat(keyFile); err != nil {
				slog.Warn("TLS_KEY_FILE set but not found, falling back to HTTP", "file", keyFile, "error", err)
			} else {
				slog.Info("TLS enabled", "cert", certFile, "key", keyFile)
				tlsEnabled = true
				errCh <- srv.ListenAndServeTLS(certFile, keyFile)
			}
		}
		if !tlsEnabled {
			errCh <- srv.ListenAndServe()
		}
	}()

	slog.Info("server starting", "port", port, "max_concurrent", maxConcurrent)

	select {
	case err := <-errCh:
		if err != http.ErrServerClosed {
			slog.Error("server fatal", "error", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		slog.Info("shutting down gracefully")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Warn("graceful shutdown timed out, forcing close", "error", err)
			srv.Close()
		}
	}
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

func maxConcurrentConversions() int {
	s := os.Getenv("MAX_CONCURRENT_CONVERSIONS")
	if s == "" {
		return 4
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 4
	}
	return n
}

// ── Middleware ────────────────────────────────────────────────────────────────

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// limit returns a handler that rejects requests with 429 Too Many Requests when
// concurrency exceeds capacity.
func limit(n int, next http.HandlerFunc) http.HandlerFunc {
	if n <= 0 {
		// Unbounded (should not happen with current defaults).
		return next
	}
	sema := make(chan struct{}, n)
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case sema <- struct{}{}:
			defer func() { <-sema }()
			next(w, r)
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]string{"error": "too many concurrent requests"})
		}
	}
}

// ── Handlers ─────────────────────────────────────────────────────────────────

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write([]byte(`{"status":"ok"}`)); err != nil {
		slog.Warn("health response write failed", "error", err)
	}
}

func handleConvertImage(w http.ResponseWriter, r *http.Request) {
	hotwords := doc.ParseHotwords(r.FormValue("hotwords"))
	slog.Info("conversion started with hotwords", "hotwords_count", len(hotwords), "hotwords", hotwords)
	convertAndServe(w, r, doc.ModeImage, hotwords)
}

func handleConvertText(w http.ResponseWriter, r *http.Request) {
	slog.Info("text conversion started")
	convertAndServe(w, r, doc.ModeText, nil)
}

func handleConvertDoc(w http.ResponseWriter, r *http.Request) {
	docTmp, headerFilename, err := saveUpload(w, r)
	if err != nil {
		return
	}
	defer os.Remove(docTmp)

	// Validate file type: only OLE2 (.doc) files accepted.
	if !doc.IsOLE2File(docTmp) {
		slog.Warn("unsupported file type for /convert/doc", "filename", headerFilename)
		http.Error(w, "unsupported file type: only .doc (OLE2) files are accepted for /convert/doc", http.StatusBadRequest)
		return
	}

	slog.Info("doc conversion started", "filename", headerFilename)

	start := time.Now()
	docxPath, cleanup, err := doc.ConvertOLE2ToDocx(docTmp)
	if err != nil {
		slog.Error("doc conversion failed",
			"filename", headerFilename,
			"error", err,
			"duration_ms", time.Since(start).Milliseconds(),
		)
		http.Error(w, fmt.Sprintf("conversion failed: %v", err), http.StatusInternalServerError)
		return
	}
	defer cleanup()
	defer os.Remove(docxPath)

	slog.Info("doc conversion done",
		"filename", headerFilename,
		"output", filepath.Base(docxPath),
		"duration_ms", time.Since(start).Milliseconds(),
	)

	// Stream the generated .docx back.
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, docxFilename(headerFilename, doc.ModeImage)))

	docxF, err := os.Open(docxPath)
	if err != nil {
		slog.Error("open result failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer docxF.Close()

	if _, err := io.Copy(w, docxF); err != nil {
		slog.Warn("stream response interrupted", "error", err)
	}
}

func handleExtractTxt(w http.ResponseWriter, r *http.Request) {
	pdfTmp, filename, err := saveUpload(w, r)
	if err != nil {
		return
	}
	defer os.Remove(pdfTmp)

	slog.Info("extract text started", "filename", filename)

	start := time.Now()
	result, err := pdf.ExtractText(pdfTmp)
	if err != nil {
		slog.Error("extract text failed",
			"filename", filename,
			"error", err,
			"duration_ms", time.Since(start).Milliseconds(),
		)
		http.Error(w, fmt.Sprintf("extract text failed: %v", err), http.StatusInternalServerError)
		return
	}

	slog.Info("extract text done",
		"filename", filename,
		"text_chars", len([]rune(result.Text)),
		"ocr_confidence", result.OCRConfidence,
		"duration_ms", time.Since(start).Milliseconds(),
	)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(result)
}

func convertAndServe(w http.ResponseWriter, r *http.Request, mode doc.ConvertMode, hotwords []string) {
	pdfTmp, headerFilename, err := saveUpload(w, r)
	if err != nil {
		return
	}
	defer os.Remove(pdfTmp)

	// Validate file type: /convert/image and /convert/text require PDF.
	if !isPDFFile(pdfTmp) {
		slog.Warn("unsupported file type for conversion", "filename", headerFilename)
		http.Error(w, "unsupported file type: only PDF files are accepted for /convert/image and /convert/text", http.StatusBadRequest)
		return
	}

	slog.Info("conversion started",
		"filename", headerFilename,
		"mode", mode,
	)

	// Convert.
	convStart := time.Now()
	var docxPath string
	if len(hotwords) > 0 && mode == doc.ModeImage {
		docxPath, err = pdf.ConvertWithHotwords(pdfTmp, mode, hotwords, func(pct float64) {})
	} else {
		docxPath, err = pdf.Convert(pdfTmp, mode, func(pct float64) {})
	}
	convDuration := time.Since(convStart)

	if err != nil {
		slog.Error("conversion failed",
			"filename", headerFilename,
			"error", err,
			"duration_ms", convDuration.Milliseconds(),
			"mode", mode,
		)
		http.Error(w, fmt.Sprintf("conversion failed: %v", err), http.StatusInternalServerError)
		return
	}
	defer os.Remove(docxPath)

	slog.Info("conversion done",
		"filename", headerFilename,
		"output", filepath.Base(docxPath),
		"duration_ms", convDuration.Milliseconds(),
		"mode", mode,
	)

	// Stream the generated .docx back.
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, docxFilename(headerFilename, mode)))

	docxF, err := os.Open(docxPath)
	if err != nil {
		slog.Error("open result failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer docxF.Close()

	if _, err := io.Copy(w, docxF); err != nil {
		slog.Warn("stream response interrupted", "error", err)
	}
}

// saveUpload reads a multipart file upload and writes it to a temp file.
// On success it returns the temp file path and original filename.
// On error it writes the HTTP error and returns a non-nil err.
func saveUpload(w http.ResponseWriter, r *http.Request) (tmpPath, filename string, err error) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		slog.Warn("parse multipart failed", "error", err)
		http.Error(w, fmt.Sprintf("failed to parse form: %v", err), http.StatusBadRequest)
		return "", "", err
	}
	defer r.MultipartForm.RemoveAll()

	file, header, err := r.FormFile("file")
	if err != nil {
		slog.Warn("missing file field", "error", err)
		http.Error(w, "missing file field", http.StatusBadRequest)
		return "", "", err
	}
	defer file.Close()

	// Preserve the original extension for correct MIME/magic detection.
	ext := filepath.Ext(header.Filename)
	if ext == "" {
		ext = ".tmp"
	}
	tmp, err := os.CreateTemp("", "docutil-*"+ext)
	if err != nil {
		slog.Error("create temp file failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return "", "", err
	}

	if _, err := io.Copy(tmp, file); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		slog.Error("write temp file failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return "", "", err
	}
	tmp.Close()

	slog.Info("upload saved",
		"filename", header.Filename,
		"size_bytes", header.Size,
	)
	return tmp.Name(), header.Filename, nil
}

// isPDFFile checks whether the file at path is a PDF by magic bytes.
func isPDFFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	var magic [4]byte
	n, _ := io.ReadFull(f, magic[:])
	if n < 4 {
		return false
	}
	// "%PDF"
	return magic[0] == '%' && magic[1] == 'P' && magic[2] == 'D' && magic[3] == 'F'
}

func docxFilename(pdfName string, mode doc.ConvertMode) string {
	ext := filepath.Ext(pdfName)
	base := pdfName[:len(pdfName)-len(ext)]
	if mode == doc.ModeText {
		return base + ".text.docx"
	}
	return base + ".docx"
}
