package pdfconv

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif" // register GIF decoder
	"image/jpeg"
	_ "image/png" // register PNG decoder
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	_ "golang.org/x/image/bmp"  // register BMP decoder
	_ "golang.org/x/image/tiff" // register TIFF decoder
	_ "golang.org/x/image/webp" // register WebP decoder

	"docutil/internal/drawutil"
	"docutil/internal/htmlpos"
	"docutil/internal/ocr"

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
	return ConvertWithHotwords(pdfPath, mode, nil, progressFn)
}

// ConvertWithHotwords converts a PDF file to a DOCX with optional hotword
// annotation (red underline) in image mode. hotwords are case-insensitive.
func ConvertWithHotwords(pdfPath string, mode ConvertMode, hotwords []string, progressFn func(float64)) (string, error) {
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
		return convertToImage(doc, pdfPath, numPages, hotwords, progressFn)
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
	return ConvertImageWithHotwords(imgPath, mode, nil, progressFn)
}

// ConvertImageWithHotwords converts a single image file to a DOCX with optional
// hotword annotation. For image files, hotwords require OCR to locate text
// positions on the image.
func ConvertImageWithHotwords(imgPath string, mode ConvertMode, hotwords []string, progressFn func(float64)) (string, error) {
	rgba, err := loadImage(imgPath)
	if err != nil {
		return "", err
	}

	switch mode {
	case ModeImage:
		// Annotate hotwords on the image (using OCR for text position).
		if len(hotwords) > 0 {
			annotateImageWithOCR(rgba, hotwords)
		}

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

func convertToImage(doc *fitz.Document, pdfPath string, numPages int, hotwords []string, progressFn func(float64)) (string, error) {
	const dpi = 150.0
	db := &docxBuilder{}

	// OCR engine is lazily initialised for scanned pages.
	var ocrEngine *ocr.Engine

	for i := 0; i < numPages; i++ {
		img, err := doc.ImageDPI(i, dpi)
		if err != nil {
			return "", fmt.Errorf("render page %d: %w", i+1, err)
		}

		// Annotate hotwords if requested.
		if len(hotwords) > 0 {
			pageText, _ := doc.Text(i)
			if strings.TrimSpace(pageText) != "" {
				// Has text layer — use MuPDF HTML positions.
				annotateImageWithMuPDF(img, doc, i, dpi, hotwords)
			} else {
				// Scanned page — use OCR with bounding boxes.
				if ocrEngine == nil {
				ocrEngine, err = initOCREngine()
				if err != nil {
					// OCR unavailable — skip annotation for this page.
					slog.Warn("OCR not available for hotword annotation", "page", i+1, "error", err)
				}
			}
				if ocrEngine != nil {
					annotateImageWithOCR(img, hotwords, ocrEngine)
				}
			}
		}

		jpgData, w, h, err := encodeJPEG(img)
		if err != nil {
			return "", fmt.Errorf("encode page %d: %w", i+1, err)
		}
		db.add(jpgData, w, h)

		progressFn(float64(i+1) / float64(numPages))
	}

	if ocrEngine != nil {
		ocrEngine.Release()
	}

	docxPath := outputPath(pdfPath)
	if err := saveDocx(docxPath, db); err != nil {
		return "", fmt.Errorf("save docx: %w", err)
	}
	return docxPath, nil
}

// ── Hotword annotation helpers ───────────────────────────────────────────────

// annotateImageWithMuPDF uses MuPDF's structured text HTML to find hotword
// positions on an already-rendered page image (at the given DPI).
func annotateImageWithMuPDF(img *image.RGBA, doc *fitz.Document, pageNum int, dpi float64, hotwords []string) {
	htmlContent, err := doc.HTML(pageNum, false)
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

// annotateImageWithOCR runs OCR with bounding boxes and draws underlines for
// hotword matches. Uses the provided engine if non-nil, otherwise creates one.
func annotateImageWithOCR(rgba *image.RGBA, hotwords []string, engine ...*ocr.Engine) {
	var eng *ocr.Engine
	if len(engine) > 0 && engine[0] != nil {
		eng = engine[0]
	} else {
		var err error
		eng, err = initOCREngine()
		if err != nil {
			return
		}
		defer eng.Release()
	}

	mat := ocr.NewMatFromRGBA(rgba)
	if mat == nil {
		return
	}
	defer mat.Release()

	lines, err := eng.RecognizeWithBoxes(mat)
	if err != nil || len(lines) == 0 {
		return
	}

	for _, line := range lines {
		cleaned := cleanOCRLine(line.Text)
		if cleaned == "" {
			continue
		}

		for _, hw := range hotwords {
			if hw == "" {
				continue
			}
			matches := findHotwordInString(cleaned, hw, line.Box)
			for _, m := range matches {
				drawutil.DrawUnderline(rgba, m.LeftPx, m.BottomPx, m.WidthPx, 2)
			}
		}
	}
}

// findHotwordInString searches for a hotword (case-insensitive) in text and
// returns pixel positions based on the character position within the given
// OCR box.
func findHotwordInString(text, hotword string, box ocr.OCRBox) []htmlpos.MatchResult {
	hwLower := strings.ToLower(hotword)
	textLower := strings.ToLower(text)

	if len(hwLower) == 0 || len(textLower) == 0 {
		return nil
	}

	// Calculate the box boundaries in pixel coordinates.
	var minX, maxX, minY, maxY float64
	minX, minY = box.Points[0].X, box.Points[0].Y
	maxX, maxY = box.Points[0].X, box.Points[0].Y
	for _, p := range box.Points {
		if p.X < minX {
			minX = p.X
		}
		if p.X > maxX {
			maxX = p.X
		}
		if p.Y < minY {
			minY = p.Y
		}
		if p.Y > maxY {
			maxY = p.Y
		}
	}

	boxW := maxX - minX

	// Work entirely in runes for correct multi-byte handling.
	runes := []rune(textLower)
	hwRunes := []rune(hwLower)
	if len(hwRunes) == 0 || len(runes) == 0 {
		return nil
	}

	charW := boxW / float64(len(runes))

	var results []htmlpos.MatchResult

	// Sliding window search (rune-indexed).
	for i := 0; i <= len(runes)-len(hwRunes); i++ {
		match := true
		for j := 0; j < len(hwRunes); j++ {
			if runes[i+j] != hwRunes[j] {
				match = false
				break
			}
		}
		if match {
			leftPx := int(minX + float64(i)*charW)
			widthPx := int(float64(len(hwRunes)) * charW)
			bottomPx := int(maxY)

			results = append(results, htmlpos.MatchResult{
				LeftPx:   leftPx,
				BottomPx: bottomPx,
				WidthPx:  widthPx,
			})
		}
	}
	return results
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
	lines, _ := runOCRPageWithConfidence(engine, img)
	return lines
}

// runOCRPageWithConfidence runs OCR on a page image and returns the cleaned
// text lines plus the average confidence (0–1), weighted by line length.
func runOCRPageWithConfidence(engine *ocr.Engine, img *image.RGBA) ([]string, float64) {
	mat := ocr.NewMatFromRGBA(img)
	if mat == nil {
		return nil, 0
	}
	defer mat.Release()

	pageText, err := engine.Recognize(mat)
	if err != nil {
		slog.Error("OCR page failed", "error", err)
		return nil, 0
	}

	// Clean and merge recognized text lines.
	var cleaned []string
	var weightedConf float64
	var totalChars int
	for _, l := range pageText.Lines {
		t := cleanOCRLine(l.Text)
		if t == "" {
			continue
		}
		cleaned = append(cleaned, t)

		chars := len([]rune(t))
		weightedConf += float64(l.Confidence) * float64(chars)
		totalChars += chars
	}

	if totalChars == 0 {
		return cleaned, 0
	}
	return cleaned, weightedConf / float64(totalChars)
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

// ── Hotword parsing utilities ───────────────────────────────────────────────

// ParseHotwords splits a user-input string into individual hotwords.
// Supported delimiters: comma, semicolon, Chinese comma/fullwidth comma,
// newline, and whitespace (as secondary delimiter after the primary ones).
func ParseHotwords(input string) []string {
	if input == "" {
		return nil
	}

	// Split by primary delimiters first.
	parts := splitAny(input, []string{",", ";", "，", "；", "\n", "\r\n"})
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func splitAny(s string, seps []string) []string {
	// Replace each separator with a null byte, then split on null.
	result := s
	for _, sep := range seps {
		result = strings.ReplaceAll(result, sep, "\x00")
	}
	var out []string
	for _, part := range strings.Split(result, "\x00") {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}