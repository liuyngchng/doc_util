package doc

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
	"strings"

	_ "golang.org/x/image/bmp"  // register BMP decoder
	_ "golang.org/x/image/tiff" // register TIFF decoder
	_ "golang.org/x/image/webp" // register WebP decoder

	"docutil/internal/drawutil"
	"docutil/internal/htmlpos"
	"docutil/internal/ocr"
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

// ── Image file conversion ─────────────────────────────────────────────────────

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
			AnnotateImageWithOCR(rgba, hotwords)
		}

		jpgData, w, h, err := EncodeJPEG(rgba)
		if err != nil {
			return "", fmt.Errorf("encode image: %w", err)
		}

		db := &DocxBuilder{}
		db.Add(jpgData, w, h)

		docxPath := OutputPath(imgPath)
		if err := SaveDocx(docxPath, db); err != nil {
			return "", fmt.Errorf("save docx: %w", err)
		}
		progressFn(1)
		return docxPath, nil

	case ModeText:
		ocrEngine, err := InitOCREngine()
		if err != nil {
			return "", fmt.Errorf("OCR is not available: %w", err)
		}
		defer ocrEngine.Release()

		db := &TextDocxBuilder{}
		db.AddPage(RunOCRPage(ocrEngine, rgba))

		docxPath := TextOutputPath(imgPath)
		if err := SaveTextDocx(docxPath, db); err != nil {
			return "", fmt.Errorf("save text docx: %w", err)
		}
		progressFn(1)
		return docxPath, nil

	default:
		return "", fmt.Errorf("unknown convert mode: %d", mode)
	}
}

// ── JPEG encode ────────────────────────────────────────────────────────────────

// EncodeJPEG encodes an RGBA image as JPEG with quality 80.
func EncodeJPEG(img *image.RGBA) ([]byte, int, int, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), img.Bounds().Dx(), img.Bounds().Dy(), nil
}

// ── Output paths ──────────────────────────────────────────────────────────────

// OutputPath derives a .docx path from the input file path.
func OutputPath(pdfPath string) string {
	dir := filepath.Dir(pdfPath)
	base := filepath.Base(pdfPath)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	return filepath.Join(dir, name+".docx")
}

// TextOutputPath derives a .text.docx path from the input file path.
func TextOutputPath(pdfPath string) string {
	dir := filepath.Dir(pdfPath)
	base := filepath.Base(pdfPath)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	return filepath.Join(dir, name+".text.docx")
}

// ── Hotword annotation ────────────────────────────────────────────────────────

// AnnotateImageWithOCR runs OCR with bounding boxes and draws underlines for
// hotword matches. If no engine is provided, one is created and released
// internally.
func AnnotateImageWithOCR(rgba *image.RGBA, hotwords []string, engine ...*ocr.Engine) {
	var eng *ocr.Engine
	if len(engine) > 0 && engine[0] != nil {
		eng = engine[0]
	} else {
		var err error
		eng, err = InitOCREngine()
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
		cleaned := CleanOCRLine(line.Text)
		if cleaned == "" {
			continue
		}

		for _, hw := range hotwords {
			if hw == "" {
				continue
			}
			matches := FindHotwordInString(cleaned, hw, line.Box)
			for _, m := range matches {
				drawutil.DrawUnderline(rgba, m.LeftPx, m.BottomPx, m.WidthPx, 2)
			}
		}
	}
}

// FindHotwordInString searches for a hotword (case-insensitive) in text and
// returns pixel positions based on the character position within the given
// OCR box.
func FindHotwordInString(text, hotword string, box ocr.OCRBox) []htmlpos.MatchResult {
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

// ── Hotword parsing ────────────────────────────────────────────────────────────

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

// ── internal helpers ──────────────────────────────────────────────────────────

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