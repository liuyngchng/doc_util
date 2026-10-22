package doc

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"docutil/internal/ocr"
)

// ── OCR engine initialisation ─────────────────────────────────────────────────

// InitOCREngine loads the PaddleOCR detection + recognition models.
func InitOCREngine() (*ocr.Engine, error) {
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

// ── OCR page helpers ──────────────────────────────────────────────────────────

// RunOCRPage runs OCR on a page image and returns the cleaned text lines.
func RunOCRPage(engine *ocr.Engine, img *image.RGBA) []string {
	lines, _ := RunOCRPageWithConfidence(engine, img)
	return lines
}

// RunOCRPageWithConfidence runs OCR on a page image and returns the cleaned
// text lines plus the average confidence (0–1), weighted by line length.
func RunOCRPageWithConfidence(engine *ocr.Engine, img *image.RGBA) ([]string, float64) {
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
		t := CleanOCRLine(l.Text)
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

// ── Seal detection ────────────────────────────────────────────────────────────

// RunSealDetection runs seal detection on a page image and OCRs any detected
// seal regions. sealPtr is a lazily-initialised *ocr.SealDetector pointer.
// Returns cleaned text lines from the seal regions.
func RunSealDetection(sealPtr **ocr.SealDetector, engine *ocr.Engine, img *image.RGBA) []string {
	if sealPtr == nil || engine == nil {
		return nil
	}

	baseDir, _ := os.Getwd()
	if exe, err := os.Executable(); err == nil {
		baseDir = filepath.Dir(exe)
	}
	if !ocr.CheckSealModel(baseDir) {
		return nil
	}

	if *sealPtr == nil {
		var err error
		*sealPtr, err = ocr.NewSealDetector(ocr.SealModelPath(baseDir))
		if err != nil {
			slog.Warn("seal detection model not available", "error", err)
			return nil
		}
	}

	mat := ocr.NewMatFromRGBA(img)
	if mat == nil {
		return nil
	}
	defer mat.Release()

	boxes, err := (*sealPtr).Detect(mat)
	if err != nil || len(boxes) == 0 {
		return nil
	}

	var sealTexts []string
	for _, box := range boxes {
		lines, err := engine.RecognizeRegion(mat, box)
		if err != nil {
			continue
		}
		for _, line := range lines {
			cleaned := CleanOCRLine(line.Text)
			if cleaned != "" {
				sealTexts = append(sealTexts, cleaned)
			}
		}
	}
	return sealTexts
}

// ── Common string helpers ─────────────────────────────────────────────────────

// NonEmptyLines splits text by newlines and returns only non-blank lines.
func NonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// ── OCR text cleanup ─────────────────────────────────────────────────────────

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

// CleanOCRLine removes PP-OCR/CRNN CTC decoder artifacts from a recognized
// text line: model delimiter characters, hyphen-with-whitespace separators,
// and CJK inter-character spaces.
func CleanOCRLine(s string) string {
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

// ── Image OCR from byte slices ────────────────────────────────────────────────

// OCRImageBytesWithSeal decodes an image and runs OCR + seal detection.
// Returns cleaned text lines, average confidence, and any seal texts.
func OCRImageBytesWithSeal(data []byte, enginePtr **ocr.Engine, sealPtr **ocr.SealDetector) ([]string, float64, []string, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, nil, fmt.Errorf("decode image: %w", err)
	}

	rgba := image.NewRGBA(src.Bounds())
	draw.Draw(rgba, rgba.Bounds(), src, src.Bounds().Min, draw.Src)

	if *enginePtr == nil {
		*enginePtr, err = InitOCREngine()
		if err != nil {
			return nil, 0, nil, fmt.Errorf("init OCR: %w", err)
		}
	}

	lines, conf := RunOCRPageWithConfidence(*enginePtr, rgba)
	if len(lines) == 0 && sealPtr == nil {
		return nil, 0, nil, fmt.Errorf("OCR produced no text")
	}

	// Seal detection on the same image.
	var sealTexts []string
	if sealPtr != nil {
		sealTexts = RunSealDetection(sealPtr, *enginePtr, rgba)
	}

	return lines, conf, sealTexts, nil
}