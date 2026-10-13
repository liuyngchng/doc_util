package pdfconv

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif" // register GIF decoder
	"image/jpeg"
	_ "image/png" // register PNG decoder
	"os"
	"path/filepath"
	"regexp"
	"strings"

	_ "golang.org/x/image/bmp"  // register BMP decoder
	_ "golang.org/x/image/tiff" // register TIFF decoder
	_ "golang.org/x/image/webp" // register WebP decoder

	"pdftoword/internal/ocr"

	"github.com/gen2brain/go-fitz"
)

// ConvertMode selects the conversion strategy.
type ConvertMode int

const (
	// ModeImage renders each page as a JPEG and embeds it in a DOCX (always works).
	ModeImage ConvertMode = iota
	// ModeText extracts text via MuPDF for pages with a text layer, and falls back
	// to OCR for image-only (scanned) pages.
	ModeText
)

// Convert converts a PDF file to a DOCX, calling progressFn with completion
// ratio (0.0–1.0) after each page.
func Convert(pdfPath string, mode ConvertMode, progressFn func(float64)) (string, error) {
	doc, err := fitz.New(pdfPath)
	if err != nil {
		return "", fmt.Errorf("open PDF: %w", err)
	}
	defer doc.Close()

	numPages := doc.NumPage()
	if numPages == 0 {
		return "", fmt.Errorf("PDF has no pages")
	}

	switch mode {
	case ModeImage:
		return convertToImage(doc, pdfPath, numPages, progressFn)
	case ModeText:
		return convertToText(doc, pdfPath, numPages, progressFn)
	default:
		return "", fmt.Errorf("unknown convert mode: %d", mode)
	}
}

// ── Image file entry point ───────────────────────────────────────────────────

// ConvertImage converts a single image file (JPG/PNG/BMP/GIF/TIFF/WebP) to a
// DOCX. ModeImage embeds the image on one page; ModeText runs OCR on it.
func ConvertImage(imgPath string, mode ConvertMode, progressFn func(float64)) (string, error) {
	rgba, err := loadImage(imgPath)
	if err != nil {
		return "", err
	}

	switch mode {
	case ModeImage:
		jpgData, w, h, err := encodeJPEG(rgba)
		if err != nil {
			return "", fmt.Errorf("encode image: %w", err)
		}

		db := &docxBuilder{}
		db.add(jpgData, w, h)

		docxPath := outputPath(imgPath)
		if err := saveDocx(docxPath, db); err != nil {
			return "", fmt.Errorf("save docx: %w", err)
		}
		progressFn(1)
		return docxPath, nil

	case ModeText:
		ocrEngine, err := initOCREngine()
		if err != nil {
			return "", fmt.Errorf("OCR is not available: %w", err)
		}
		defer ocrEngine.Release()

		db := &textDocxBuilder{}
		db.addPage(runOCRPage(ocrEngine, rgba))

		docxPath := textOutputPath(imgPath)
		if err := saveTextDocx(docxPath, db); err != nil {
			return "", fmt.Errorf("save text docx: %w", err)
		}
		progressFn(1)
		return docxPath, nil

	default:
		return "", fmt.Errorf("unknown convert mode: %d", mode)
	}
}

// loadImage decodes an image file and returns it as RGBA.
func loadImage(path string) (*image.RGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open image: %w", err)
	}
	defer f.Close()

	src, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}

	rgba := image.NewRGBA(src.Bounds())
	draw.Draw(rgba, rgba.Bounds(), src, src.Bounds().Min, draw.Src)
	return rgba, nil
}

// ── Image mode (screenshot) ─────────────────────────────────────────────────

func convertToImage(doc *fitz.Document, pdfPath string, numPages int, progressFn func(float64)) (string, error) {
	const dpi = 150.0
	db := &docxBuilder{}

	for i := 0; i < numPages; i++ {
		img, err := doc.ImageDPI(i, dpi)
		if err != nil {
			return "", fmt.Errorf("render page %d: %w", i+1, err)
		}

		jpgData, w, h, err := encodeJPEG(img)
		if err != nil {
			return "", fmt.Errorf("encode page %d: %w", i+1, err)
		}
		db.add(jpgData, w, h)

		progressFn(float64(i+1) / float64(numPages))
	}

	docxPath := outputPath(pdfPath)
	if err := saveDocx(docxPath, db); err != nil {
		return "", fmt.Errorf("save docx: %w", err)
	}
	return docxPath, nil
}

// ── Text mode (extract or OCR) ──────────────────────────────────────────────

func convertToText(doc *fitz.Document, pdfPath string, numPages int, progressFn func(float64)) (string, error) {
	db := &textDocxBuilder{}

	// OCR engine is lazily initialised when a page has no text.
	var ocrEngine *ocr.Engine

	for i := 0; i < numPages; i++ {
		pageText, err := doc.Text(i)
		if err != nil {
			return "", fmt.Errorf("extract text page %d: %w", i+1, err)
		}

		lines := nonEmptyLines(pageText)
		if len(lines) > 0 {
			// This page has a text layer — use it directly.
			// NOTE: MuPDF's Text() outputs blocks in PDF content-stream
			// order, NOT visual (top-to-bottom) order.  Page footers
			// may appear before body text.  A proper fix would require
			// layout analysis (bbox sorting / AI-based zoning).
			db.addPage(lines)
		} else {
			// No text layer — fall back to OCR.
			if ocrEngine == nil {
				ocrEngine, err = initOCREngine()
				if err != nil {
					return "", fmt.Errorf("page %d has no text layer and OCR is not available: %w", i+1, err)
				}
				defer ocrEngine.Release()
			}

			img, err := doc.ImageDPI(i, 150)
			if err != nil {
				return "", fmt.Errorf("render page %d for OCR: %w", i+1, err)
			}

			ocrTexts := runOCRPage(ocrEngine, img)
			db.addPage(ocrTexts)
		}

		progressFn(float64(i+1) / float64(numPages))
	}

	docxPath := textOutputPath(pdfPath)
	if err := saveTextDocx(docxPath, db); err != nil {
		return "", fmt.Errorf("save text docx: %w", err)
	}
	return docxPath, nil
}

// nonEmptyLines splits text by newlines and returns only non-blank lines.
func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// initOCREngine loads the PaddleOCR detection + recognition models.
func initOCREngine() (*ocr.Engine, error) {
	baseDir, _ := os.Getwd()
	if exe, err := os.Executable(); err == nil {
		baseDir = filepath.Dir(exe)
	}
	if !ocr.CheckModels(baseDir) {
		return nil, fmt.Errorf("OCR models not found in %s", ocr.ModelsDir(baseDir))
	}
	engine, err := ocr.NewEngineWithConfig(ocr.ModelsDir(baseDir), ocr.DefaultConfig())
	if err != nil {
		return nil, fmt.Errorf("load OCR engine: %w", err)
	}
	return engine, nil
}

// ── OCR page helper ─────────────────────────────────────────────────────────

func runOCRPage(engine *ocr.Engine, img *image.RGBA) []string {
	mat := ocr.NewMatFromRGBA(img)
	if mat == nil {
		return nil
	}
	defer mat.Release()

	pageText, err := engine.Recognize(mat)
	if err != nil {
		fmt.Fprintf(os.Stderr, "OCR page failed: %v\n", err)
		return nil
	}

	// Clean and merge recognized text lines.
	var cleaned []string
	for _, l := range pageText.Lines {
		t := cleanOCRLine(l.Text)
		if t == "" {
			continue
		}
		cleaned = append(cleaned, t)
	}
	return cleaned
}

// ── OCR text cleanup ────────────────────────────────────────────────────────

// Separator artifacts produced by the PP-OCR/CRNN CTC decoder: the model
// often emits ASCII hyphens with whitespace between every character in a
// run (e.g. "2- 0- 2- 6", "A- I", "公- 司", "办- 【").  Legitimate
// hyphens ("hello-world", "A-1") have no whitespace on either side and
// are preserved.  Em/en dashes (— – －) are not touched.
var (
	reHyphenArtifact = regexp.MustCompile(`(\S)\s*-\s+(\S)|(\S)\s+-\s*(\S)`)
	reLeadHyphen     = regexp.MustCompile(`^\s*-\s*`)
	reTrailHyphen    = regexp.MustCompile(`\s*-\s*$`)
	reCJKSpace       = regexp.MustCompile(`(\p{Han})\s+(\p{Han})`)
)

func cleanOCRLine(s string) string {
	// Strip ' characters (model delimiter/padding character in PP-OCR).
	s = strings.ReplaceAll(s, "'", "")
	// Non-overlapping regex — loop until stable.
	for {
		prev := s
		s = reHyphenArtifact.ReplaceAllString(s, "$1$2$3$4")
		s = reLeadHyphen.ReplaceAllString(s, "")
		s = reTrailHyphen.ReplaceAllString(s, "")
		s = reCJKSpace.ReplaceAllString(s, "$1$2")
		if s == prev {
			break
		}
	}
	return strings.TrimSpace(s)
}

// ── JPEG encode ─────────────────────────────────────────────────────────────

func encodeJPEG(img *image.RGBA) ([]byte, int, int, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), img.Bounds().Dx(), img.Bounds().Dy(), nil
}

// ── Output paths ────────────────────────────────────────────────────────────

func outputPath(pdfPath string) string {
	dir := filepath.Dir(pdfPath)
	base := filepath.Base(pdfPath)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	return filepath.Join(dir, name+".docx")
}

func textOutputPath(pdfPath string) string {
	dir := filepath.Dir(pdfPath)
	base := filepath.Base(pdfPath)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	return filepath.Join(dir, name+".text.docx")
}