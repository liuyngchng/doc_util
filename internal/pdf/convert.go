package pdf

import (
	"context"
	"fmt"
	"image"
	"log/slog"
	"strings"

	"docutil/internal/doc"
	"docutil/internal/drawutil"
	"docutil/internal/htmlpos"
	"docutil/internal/ocr"

	"github.com/gen2brain/go-fitz"
)

// Convert converts a PDF file to a DOCX, calling progressFn with completion
// ratio (0.0–1.0) after each page.
func Convert(pdfPath string, mode doc.ConvertMode, progressFn func(float64)) (string, error) {
	return ConvertWithHotwords(pdfPath, mode, nil, progressFn)
}

// ConvertWithHotwords converts a PDF file to a DOCX with optional hotword
// annotation (red underline) in image mode. hotwords are case-insensitive.
func ConvertWithHotwords(pdfPath string, mode doc.ConvertMode, hotwords []string, progressFn func(float64)) (string, error) {
	return ConvertContext(context.Background(), pdfPath, mode, hotwords, progressFn)
}

// ConvertContext is like ConvertWithHotwords but accepts a context for
// cancellation. Long-running conversions check ctx.Err() between pages and
// return early with the context error when cancelled.
func ConvertContext(ctx context.Context, pdfPath string, mode doc.ConvertMode, hotwords []string, progressFn func(float64)) (string, error) {
	fd, err := fitz.New(pdfPath)
	if err != nil {
		return "", fmt.Errorf("open PDF: %w", err)
	}
	defer fd.Close()

	numPages := fd.NumPage()
	if numPages == 0 {
		return "", fmt.Errorf("PDF has no pages")
	}

	switch mode {
	case doc.ModeImage:
		return convertToImage(ctx, fd, pdfPath, numPages, hotwords, progressFn)
	case doc.ModeText:
		return convertToText(ctx, fd, pdfPath, numPages, progressFn)
	default:
		return "", fmt.Errorf("unknown convert mode: %d", mode)
	}
}

// ── Image mode (screenshot) ─────────────────────────────────────────────────

func convertToImage(ctx context.Context, fd *fitz.Document, pdfPath string, numPages int, hotwords []string, progressFn func(float64)) (string, error) {
	const dpi = 150.0
	db := &doc.DocxBuilder{}

	// OCR engine is lazily initialised for scanned pages.
	var ocrEngine *ocr.Engine
	defer func() {
		if ocrEngine != nil {
			ocrEngine.Release()
		}
	}()

	for i := 0; i < numPages; i++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		img, err := fd.ImageDPI(i, dpi)
		if err != nil {
			return "", fmt.Errorf("render page %d: %w", i+1, err)
		}

		// Annotate hotwords if requested.
		if len(hotwords) > 0 {
			pageText, _ := fd.Text(i)
			if strings.TrimSpace(pageText) != "" {
				// Has text layer — use MuPDF HTML positions.
				annotateImageWithMuPDF(img, fd, i, dpi, hotwords)
			} else {
				// Scanned page — use OCR with bounding boxes.
				if ocrEngine == nil {
					ocrEngine, err = doc.InitOCREngine()
					if err != nil {
						// OCR unavailable — skip annotation for this page.
						slog.Warn("OCR not available for hotword annotation", "page", i+1, "error", err)
					}
				}
				if ocrEngine != nil {
					doc.AnnotateImageWithOCR(img, hotwords, ocrEngine)
				}
			}
		}

		jpgData, w, h, err := doc.EncodeJPEG(img)
		if err != nil {
			return "", fmt.Errorf("encode page %d: %w", i+1, err)
		}
		db.Add(jpgData, w, h)

		progressFn(float64(i+1) / float64(numPages))
	}

	docxPath := doc.OutputPath(pdfPath)
	if err := doc.SaveDocx(docxPath, db); err != nil {
		return "", fmt.Errorf("save docx: %w", err)
	}
	return docxPath, nil
}

// ── Hotword annotation via MuPDF ──────────────────────────────────────────────

// annotateImageWithMuPDF uses MuPDF's structured text HTML to find hotword
// positions on an already-rendered page image (at the given DPI).
func annotateImageWithMuPDF(img *image.RGBA, fd *fitz.Document, pageNum int, dpi float64, hotwords []string) {
	htmlContent, err := fd.HTML(pageNum, false)
	if err != nil {
		return
	}

	spans := htmlpos.ParsePageHTML(htmlContent)
	if len(spans) == 0 {
		return
	}

	for _, hw := range hotwords {
		if hw == "" {
			continue
		}
		matches := htmlpos.FindHotwordMatches(spans, hw, dpi)
		for _, m := range matches {
			// Underline is 1px thick at 150dpi; scale up for higher DPI.
			thickness := int(dpi/150 + 0.5)
			if thickness < 1 {
				thickness = 1
			}
			// Position the underline 1px below the text baseline.
			drawutil.DrawUnderline(img, m.LeftPx, m.BottomPx+1, m.WidthPx, thickness)
		}
	}
}

// ── Text mode (extract or OCR) ──────────────────────────────────────────────

func convertToText(ctx context.Context, fd *fitz.Document, pdfPath string, numPages int, progressFn func(float64)) (string, error) {
	db := &doc.TextDocxBuilder{}

	// OCR engine is lazily initialised when a page has no text.
	var ocrEngine *ocr.Engine
	defer func() {
		if ocrEngine != nil {
			ocrEngine.Release()
		}
	}()

	for i := 0; i < numPages; i++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		pageText, err := fd.Text(i)
		if err != nil {
			return "", fmt.Errorf("extract text page %d: %w", i+1, err)
		}

		lines := doc.NonEmptyLines(pageText)
		if len(lines) > 0 {
			// This page has a text layer — use it directly.
			// NOTE: MuPDF's Text() outputs blocks in PDF content-stream
			// order, NOT visual (top-to-bottom) order.  Page footers
			// may appear before body text.  A proper fix would require
			// layout analysis (bbox sorting / AI-based zoning).
			db.AddPage(lines)
		} else {
			// No text layer — fall back to OCR.
			if ocrEngine == nil {
				ocrEngine, err = doc.InitOCREngine()
				if err != nil {
					return "", fmt.Errorf("page %d has no text layer and OCR is not available: %w", i+1, err)
				}
			}

			img, err := fd.ImageDPI(i, 150)
			if err != nil {
				return "", fmt.Errorf("render page %d for OCR: %w", i+1, err)
			}

			ocrTexts := doc.RunOCRPage(ocrEngine, img)
			db.AddPage(ocrTexts)
		}

		progressFn(float64(i+1) / float64(numPages))
	}

	docxPath := doc.TextOutputPath(pdfPath)
	if err := doc.SaveTextDocx(docxPath, db); err != nil {
		return "", fmt.Errorf("save text docx: %w", err)
	}
	return docxPath, nil
}