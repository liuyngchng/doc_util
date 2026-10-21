// Command server is a headless HTTP server that converts PDF to DOCX.
//
// Build (Docker, with OCR):
//
//	./build_server.sh
//
// The server exposes three endpoints:
//
//	POST /convert/image  → screenshot DOCX (always works)
//	POST /convert/text   → text-layer DOCX (extract text for native PDFs, OCR for scans)
//	POST /extract/txt    → plain text string (PDF or DOCX input; OCR for image-only content)
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"pdftoword/internal/pdfconv"
)

func main() {
	slogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slogLevel(),
	}))
	slog.SetDefault(slogger)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /convert/image", handleConvertImage)
	mux.HandleFunc("POST /convert/text", handleConvertText)
	mux.HandleFunc("POST /extract/txt", handleExtractTxt)
	mux.HandleFunc("GET /health", handleHealth)

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      withLogging(mux),
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 300 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	slog.Info("server starting", "port", port)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server fatal", "error", err)
		os.Exit(1)
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

// ── Handlers ─────────────────────────────────────────────────────────────────

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

func handleConvertImage(w http.ResponseWriter, r *http.Request) {
	hotwords := pdfconv.ParseHotwords(r.FormValue("hotwords"))
	slog.Info("conversion started with hotwords", "hotwords_count", len(hotwords), "hotwords", hotwords)
	convertAndServe(w, r, pdfconv.ModeImage, hotwords)
}

func handleConvertText(w http.ResponseWriter, r *http.Request) {
	convertAndServe(w, r, pdfconv.ModeText, nil)
}

func handleExtractTxt(w http.ResponseWriter, r *http.Request) {
	pdfTmp, _, err := saveUpload(w, r)
	if err != nil {
		return
	}
	defer os.Remove(pdfTmp)

	result, err := pdfconv.ExtractText(pdfTmp)
	if err != nil {
		slog.Error("extract text failed", "error", err)
		http.Error(w, fmt.Sprintf("extract text failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(result)
}

func convertAndServe(w http.ResponseWriter, r *http.Request, mode pdfconv.ConvertMode, hotwords []string) {
	pdfTmp, headerFilename, err := saveUpload(w, r)
	if err != nil {
		return
	}
	defer os.Remove(pdfTmp)

	slog.Info("conversion started",
		"filename", headerFilename,
		"mode", mode,
	)

	// Convert.
	convStart := time.Now()
	var docxPath string
	if len(hotwords) > 0 && mode == pdfconv.ModeImage {
		docxPath, err = pdfconv.ConvertWithHotwords(pdfTmp, mode, hotwords, func(pct float64) {})
	} else {
		docxPath, err = pdfconv.Convert(pdfTmp, mode, func(pct float64) {})
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
	tmp, err := os.CreateTemp("", "pdf2docx-*"+ext)
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

func docxFilename(pdfName string, mode pdfconv.ConvertMode) string {
	ext := filepath.Ext(pdfName)
	base := pdfName[:len(pdfName)-len(ext)]
	if mode == pdfconv.ModeText {
		return base + ".text.docx"
	}
	return base + ".docx"
}