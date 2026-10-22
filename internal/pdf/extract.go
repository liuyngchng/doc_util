package pdf

import (
	"fmt"

	"docutil/internal/doc"
	"docutil/internal/ocr"

	"github.com/gen2brain/go-fitz"
)

// ExtractText processes a PDF, DOCX, or legacy .doc file and returns its full
// text content. For image-only pages (scanned PDFs, DOCX embedded images), OCR
// is used.  The file type is auto-detected from the magic bytes; the filename
// is not used.
//
// Legacy .doc (OLE2 Compound Document) files are converted to .docx via
// LibreOffice headless (soffice) before extraction.  Requires libreoffice-writer
// to be installed on the system.
//
// Known limitations:
//   - PDF: go-fitz does not expose page-level embedded-image extraction.
//     Pages with a text layer use MuPDF text extraction directly; pages
//     without a text layer are rendered at 150 dpi and OCR'd as a whole.
//     Embedded images on pages that *also* have a text layer are ignored.
//   - DOCX: only the main document body (word/document.xml) is processed.
//     Headers, footers, footnotes, and text boxes are not extracted.
func ExtractText(path string) (*doc.ExtractResult, error) {
	if doc.IsDocx(path) {
		return doc.ExtractDocxText(path)
	}
	if doc.IsOLE2File(path) {
		return doc.ExtractOLE2Text(path)
	}
	return extractPDFText(path)
}

// ── PDF text extraction ───────────────────────────────────────────────────────

// extractPDFText extracts text from a PDF.  Each page is processed the
// same way as ModeText conversion: native MuPDF text when present,
// otherwise OCR on a 150 dpi render of the page.
//
// Seal detection runs on every page regardless of the text layer, because
// seals (stamps) are graphical and do not appear in MuPDF's text output.
func extractPDFText(path string) (*doc.ExtractResult, error) {
	fd, err := fitz.New(path)
	if err != nil {
		return nil, fmt.Errorf("open PDF: %w", err)
	}
	defer fd.Close()

	numPages := fd.NumPage()
	if numPages == 0 {
		return &doc.ExtractResult{OCRConfidence: 1.0}, nil
	}

	var ocrEngine *ocr.Engine
	var sealDetector *ocr.SealDetector
	tr := &doc.ConfTracker{}

	for i := 0; i < numPages; i++ {
		pageText, err := fd.Text(i)
		if err != nil {
			return nil, fmt.Errorf("extract text page %d: %w", i+1, err)
		}

		lines := doc.NonEmptyLines(pageText)

		// Render the page image once; reused for OCR (scanned pages) and
		// seal detection (every page).  Seals are graphical elements that
		// MuPDF's text extraction cannot see, so every page is rendered
		// regardless of the text layer.
		img, err := fd.ImageDPI(i, 150)
		if err != nil {
			return nil, fmt.Errorf("render page %d: %w", i+1, err)
		}

		if len(lines) > 0 {
			tr.AddNative(lines)
		} else {
			// No text layer — OCR. Initialise the engine first.
			if ocrEngine == nil {
				ocrEngine, err = doc.InitOCREngine()
				if err != nil {
					return nil, fmt.Errorf("page %d has no text layer and OCR is not available: %w", i+1, err)
				}
				defer ocrEngine.Release()
			}

			ocrLines, conf := doc.RunOCRPageWithConfidence(ocrEngine, img)
			tr.AddOCR(ocrLines, conf)
		}

		// Seal detection on every page.  It also needs the OCR engine for
		// seal-text recognition, so initialise it best-effort when not
		// already loaded (e.g. text-layer-only documents).
		if ocrEngine == nil {
			if eng, err := doc.InitOCREngine(); err == nil {
				ocrEngine = eng
				defer ocrEngine.Release()
			}
		}
		if ocrEngine != nil {
			sealTexts := doc.RunSealDetection(&sealDetector, ocrEngine, img)
			tr.AddSeal(sealTexts)
		}

		if i < numPages-1 {
			tr.PageSep()
		}
	}

	return tr.Result(), nil
}