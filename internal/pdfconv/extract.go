package pdfconv

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"

	"docutil/internal/ocr"

	"github.com/gen2brain/go-fitz"
)

// DOCX namespace URIs.
const (
	nsW = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	nsA = "http://schemas.openxmlformats.org/drawingml/2006/main"
	nsR = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
)

// ExtractResult holds the text extracted from a document plus the average
// OCR confidence across the document.
type ExtractResult struct {
	Text          string   `json:"text"`
	OCRConfidence float64  `json:"ocr_confidence"`
	HasSeal       bool     `json:"has_seal"`
	SealText      []string `json:"seal_text"`
}

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
func ExtractText(path string) (*ExtractResult, error) {
	if isDocx(path) {
		return extractDocxText(path)
	}
	if isOLE2File(path) {
		return extractOLE2Text(path)
	}
	return extractPDFText(path)
}

// ── File-type detection ─────────────────────────────────────────────────

// isDocx returns true when path is a DOCX (OOXML) file.
// Detection: reads the first 4 bytes — ZIP files start with "PK".
// For ZIP files, checks that the archive contains word/document.xml to
// distinguish DOCX from other ZIP-based formats (ODT, EPUB, etc.).
func isDocx(path string) bool {
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

	// ZIP magic.
	if magic[0] != 'P' || magic[1] != 'K' {
		return false
	}

	// Verify it has word/document.xml.
	info, err := f.Stat()
	if err != nil {
		return false
	}
	zr, err := zip.NewReader(f, info.Size())
	if err != nil {
		return false
	}
	for _, entry := range zr.File {
		if entry.Name == "word/document.xml" {
			return true
		}
	}
	return false
}

// isOLE2File returns true when path is an OLE2 Compound Document Binary file
// (the legacy Microsoft Office container format: .doc, .xls, .ppt, etc.).
// Detection reads the 8-byte OLE2 header magic D0 CF 11 E0 A1 B1 1A E1.
func isOLE2File(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	var magic [8]byte
	n, _ := io.ReadFull(f, magic[:])
	if n < 8 {
		return false
	}
	return bytes.Equal(magic[:], []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1})
}

// ── Legacy .doc (OLE2) extraction ────────────────────────────────────────

// sofficeTimeout caps how long a single LibreOffice conversion may run.
const sofficeTimeout = 60 * time.Second

// sofficePath returns the path to the LibreOffice binary, honouring the
// SOFFICE_PATH env var (default /usr/bin/soffice).
func sofficePath() string {
	if p := os.Getenv("SOFFICE_PATH"); p != "" {
		return p
	}
	return "/usr/bin/soffice"
}

// extractOLE2Text converts a legacy Office file (.doc/.xls/.ppt) to .docx via
// LibreOffice headless, then extracts text from the result.  All temporary files
// are cleaned up before return.
func extractOLE2Text(path string) (*ExtractResult, error) {
	docxTmp, cleanup, err := convertOLE2ToDocx(path)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	return extractDocxText(docxTmp)
}

// convertOLE2ToDocx converts a legacy Office file to .docx using LibreOffice
// headless.  It returns the path to the generated .docx file and a cleanup
// function that removes both the file and its containing temp directory.
func convertOLE2ToDocx(srcPath string) (docxPath string, cleanup func(), _ error) {
	soffice := sofficePath()
	if _, err := os.Stat(soffice); err != nil {
		return "", nil, fmt.Errorf("libreoffice not found at %s (set SOFFICE_PATH to its location): %w", soffice, err)
	}

	outDir, err := os.MkdirTemp("", "docutil-lo-*")
	if err != nil {
		return "", nil, fmt.Errorf("create temp dir: %w", err)
	}
	cleanup = func() { os.RemoveAll(outDir) }

	ctx, cancel := context.WithTimeout(context.Background(), sofficeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, soffice,
		"--headless",
		"--convert-to", "docx",
		"--outdir", outDir,
		srcPath,
	)
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		os.RemoveAll(outDir)
		if ctx.Err() == context.DeadlineExceeded {
			return "", nil, fmt.Errorf("libreoffice conversion timed out after %s: %w", sofficeTimeout, runErr)
		}
		return "", nil, fmt.Errorf("libreoffice conversion failed: %w (output: %s)", runErr, strings.TrimSpace(string(out)))
	}

	// The output file is named after the source basename with a .docx extension.
	base := filepath.Base(srcPath)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	docxPath = filepath.Join(outDir, stem+".docx")
	if _, err := os.Stat(docxPath); err != nil {
		os.RemoveAll(outDir)
		return "", nil, fmt.Errorf("libreoffice produced no .docx output (output: %s)", strings.TrimSpace(string(out)))
	}

	slog.Info("libreoffice converted legacy document", "src", base, "out", filepath.Base(docxPath))
	return docxPath, cleanup, nil
}

// ── Confidence tracking ──────────────────────────────────────────────────

// confTracker accumulates text and weighted confidence for the document.
type confTracker struct {
	sb           strings.Builder
	totalChars   int
	weightedConf float64
	sealTexts    []string
}

// addNative appends lines from a native text source (MuPDF text layer or DOCX
// paragraph text). Native text has confidence 1.0.
func (c *confTracker) addNative(lines []string) {
	for _, l := range lines {
		c.sb.WriteString(l)
		c.sb.WriteByte('\n')
		chars := len([]rune(l))
		c.weightedConf += 1.0 * float64(chars)
		c.totalChars += chars
	}
}

// addOCR appends OCR lines with the given per-line confidence.
func (c *confTracker) addOCR(lines []string, conf float64) {
	for _, l := range lines {
		c.sb.WriteString(l)
		c.sb.WriteByte('\n')
		chars := len([]rune(l))
		c.weightedConf += conf * float64(chars)
		c.totalChars += chars
	}
}

// addSeal appends seal texts found on the page.
func (c *confTracker) addSeal(texts []string) {
	c.sealTexts = append(c.sealTexts, texts...)
}

// pageSep adds a blank line between pages (no confidence contribution).
func (c *confTracker) pageSep() {
	// Only add separator when there is already content.
	if c.totalChars > 0 {
		c.sb.WriteByte('\n')
	}
}

func (c *confTracker) result() *ExtractResult {
	conf := 1.0
	if c.totalChars > 0 {
		conf = c.weightedConf / float64(c.totalChars)
	}
	hasSeal := len(c.sealTexts) > 0
	return &ExtractResult{
		Text:          c.sb.String(),
		OCRConfidence: conf,
		HasSeal:       hasSeal,
		SealText:      c.sealTexts,
	}
}

// ── PDF text extraction ─────────────────────────────────────────────────

// extractPDFText extracts text from a PDF.  Each page is processed the
// same way as ModeText conversion: native MuPDF text when present,
// otherwise OCR on a 150 dpi render of the page.
//
// Seal detection runs on every page regardless of the text layer, because
// seals (stamps) are graphical and do not appear in MuPDF's text output.
func extractPDFText(path string) (*ExtractResult, error) {
	doc, err := fitz.New(path)
	if err != nil {
		return nil, fmt.Errorf("open PDF: %w", err)
	}
	defer doc.Close()

	numPages := doc.NumPage()
	if numPages == 0 {
		return &ExtractResult{OCRConfidence: 1.0}, nil
	}

	var ocrEngine *ocr.Engine
	var sealDetector *ocr.SealDetector
	tr := &confTracker{}

	for i := 0; i < numPages; i++ {
		pageText, err := doc.Text(i)
		if err != nil {
			return nil, fmt.Errorf("extract text page %d: %w", i+1, err)
		}

		lines := nonEmptyLines(pageText)
		if len(lines) > 0 {
			tr.addNative(lines)
		} else {
			// No text layer — OCR.
			if ocrEngine == nil {
				ocrEngine, err = initOCREngine()
				if err != nil {
					return nil, fmt.Errorf("page %d has no text layer and OCR is not available: %w", i+1, err)
				}
				defer ocrEngine.Release()
			}
		}

		// Seal detection on every page.  Seals are graphical elements that
		// MuPDF's text extraction cannot see, so we must render the page
		// image whether or not a text layer exists.  If the page has a text
		// layer the render cost is extra, but it is necessary to detect
		// stamps.  On scanned pages the image is already rendered above and
		// we reuse it.
		if ocrEngine != nil {
			img, err := doc.ImageDPI(i, 150)
			if err != nil {
				return nil, fmt.Errorf("render page %d: %w", i+1, err)
			}

			// Run OCR and seal detection on the same image.
			if len(lines) == 0 {
				ocrLines, conf := runOCRPageWithConfidence(ocrEngine, img)
				tr.addOCR(ocrLines, conf)
			}

			sealTexts := runSealDetection(&sealDetector, ocrEngine, img)
			tr.addSeal(sealTexts)
		}

		if i < numPages-1 {
			tr.pageSep()
		}
	}

	return tr.result(), nil
}

// runSealDetection runs seal detection on a page image and OCRs any detected
// seal regions. sealPtr is a lazily-initialised *ocr.SealDetector pointer.
// Returns cleaned text lines from the seal regions.
func runSealDetection(sealPtr **ocr.SealDetector, engine *ocr.Engine, img *image.RGBA) []string {
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
			cleaned := cleanOCRLine(line.Text)
			if cleaned != "" {
				sealTexts = append(sealTexts, cleaned)
			}
		}
	}
	return sealTexts
}

// ── DOCX text extraction ─────────────────────────────────────────────────

// docxSegment represents a content segment inside word/document.xml.
// A segment is either a text paragraph or an embedded image reference.
type docxSegment struct {
	text  string // paragraph text (may be empty for image-only runs)
	image string // rId of the image in the .rels file, e.g. "rId5"
}

// extractDocxText extracts text from a DOCX by reading the main document
// body and OCR-ing any embedded images in document order.
func extractDocxText(path string) (*ExtractResult, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open DOCX: %w", err)
	}
	defer zr.Close()

	// 1. Parse word/_rels/document.xml.rels → rId → media path.
	relTargets, err := parseRels(zr)
	if err != nil {
		return nil, fmt.Errorf("parse rels: %w", err)
	}

	// 2. Read word/document.xml and collect segments in document order.
	docFile, err := openZipFile(zr, "word/document.xml")
	if err != nil {
		return nil, fmt.Errorf("open document.xml: %w", err)
	}
	segments, err := parseDocumentXML(docFile)
	docFile.Close()
	if err != nil {
		return nil, fmt.Errorf("parse document.xml: %w", err)
	}

	// 3. Walk segments: text → addNative; image → OCR + seal → addOCR.
	var ocrEngine *ocr.Engine
	var sealDetector *ocr.SealDetector
	defer func() {
		if ocrEngine != nil {
			ocrEngine.Release()
		}
		if sealDetector != nil {
			sealDetector.Release()
		}
	}()
	tr := &confTracker{}

	for _, seg := range segments {
		if seg.text != "" {
			tr.addNative([]string{seg.text})
			continue
		}

		// Image segment.
		if seg.image == "" {
			continue
		}

		mediaPath, ok := relTargets[seg.image]
		if !ok {
			continue
		}

		ocrLines, conf, sealTexts, err := ocrImageWithSealFromZip(zr, mediaPath, &ocrEngine, &sealDetector)
		if err != nil {
			// Skip images that cannot be decoded (e.g. EMF/WMF)
			// or produced no text and no seals.
			continue
		}
		tr.addOCR(ocrLines, conf)
		if len(sealTexts) > 0 {
			tr.addSeal(sealTexts)
		}
	}

	return tr.result(), nil
}

// ── DOCX parsing helpers ─────────────────────────────────────────────────

// parseRels reads word/_rels/document.xml.rels and returns a map from rId
// (e.g. "rId5") to the target path (e.g. "media/image1.png").
func parseRels(zr *zip.ReadCloser) (map[string]string, error) {
	const relsFile = "word/_rels/document.xml.rels"

	f, err := openZipFile(zr, relsFile)
	if err != nil {
		// No rels file means no images, which is valid.
		return map[string]string{}, nil
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", relsFile, err)
	}

	type relationship struct {
		ID     string `xml:"Id,attr"`
		Target string `xml:"Target,attr"`
	}
	type relationships struct {
		Rels []relationship `xml:"Relationship"`
	}

	var rels relationships
	if err := xml.Unmarshal(data, &rels); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", relsFile, err)
	}

	out := make(map[string]string, len(rels.Rels))
	for _, r := range rels.Rels {
		out[r.ID] = r.Target
	}
	return out, nil
}

// parseDocumentXML extracts text and image references from a DOCX
// document.xml body, preserving document order.
//
// It walks the XML tree with xml.Decoder, collecting <w:t> text runs within
// each <w:p> paragraph and detecting <a:blip> elements whose r:embed
// attribute references an image.  Each paragraph becomes one segment: text,
// an image, or (rarely) both when a paragraph mixes runs and a drawing.
func parseDocumentXML(r io.Reader) ([]docxSegment, error) {
	decoder := xml.NewDecoder(r)

	var segments []docxSegment
	var curText strings.Builder
	var lastImageRID string
	var inP, inT bool

	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case t.Name.Space == nsW && t.Name.Local == "p":
				// New paragraph.
				curText.Reset()
				lastImageRID = ""
				inP = true

			case t.Name.Space == nsW && t.Name.Local == "t":
				inT = true

			case t.Name.Space == nsW && t.Name.Local == "br":
				// Line break inside a paragraph.
				if inP {
					curText.WriteByte('\n')
				}

			case t.Name.Space == nsA && t.Name.Local == "blip":
				// Image reference: r:embed="rId5".
				if inP {
					for _, attr := range t.Attr {
						if attr.Name.Space == nsR && attr.Name.Local == "embed" {
							lastImageRID = attr.Value
						}
					}
				}
			}

		case xml.EndElement:
			switch {
			case t.Name.Space == nsW && t.Name.Local == "p":
				if inP {
					flushParagraph(&segments, &curText, lastImageRID)
				}
				inP = false
				inT = false

			case t.Name.Space == nsW && t.Name.Local == "t":
				inT = false
			}

		case xml.CharData:
			if inP && inT {
				curText.Write(t)
			}
		}
	}

	return segments, nil
}

// flushParagraph appends the accumulated paragraph content as one or two
// segments (text first, then image) and clears the accumulator.
func flushParagraph(segments *[]docxSegment, curText *strings.Builder, imageRID string) {
	if curText.Len() > 0 {
		*segments = append(*segments, docxSegment{text: curText.String()})
		curText.Reset()
	}
	if imageRID != "" {
		*segments = append(*segments, docxSegment{image: imageRID})
	}
}

// ── Image OCR helper ────────────────────────────────────────────────────

// ocrImageWithSealFromZip reads an image from a zip archive, decodes it to
// RGBA, runs OCR and seal detection on it. enginePtr and sealPtr are
// lazily-initialised. Returns cleaned text lines, average confidence, and
// any seal texts detected.
func ocrImageWithSealFromZip(zr *zip.ReadCloser, mediaPath string, enginePtr **ocr.Engine, sealPtr **ocr.SealDetector) ([]string, float64, []string, error) {
	f, err := openZipFile(zr, mediaPath)
	if err != nil {
		return nil, 0, nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("read %s: %w", mediaPath, err)
	}

	return ocrImageBytesWithSeal(data, enginePtr, sealPtr)
}

// ocrImageBytesWithSeal decodes an image and runs OCR + seal detection.
// Returns cleaned text lines, average confidence, and any seal texts.
func ocrImageBytesWithSeal(data []byte, enginePtr **ocr.Engine, sealPtr **ocr.SealDetector) ([]string, float64, []string, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, nil, fmt.Errorf("decode image: %w", err)
	}

	rgba := image.NewRGBA(src.Bounds())
	draw.Draw(rgba, rgba.Bounds(), src, src.Bounds().Min, draw.Src)

	if *enginePtr == nil {
		*enginePtr, err = initOCREngine()
		if err != nil {
			return nil, 0, nil, fmt.Errorf("init OCR: %w", err)
		}
	}

	lines, conf := runOCRPageWithConfidence(*enginePtr, rgba)
	if len(lines) == 0 && sealPtr == nil {
		return nil, 0, nil, fmt.Errorf("OCR produced no text")
	}

	// Seal detection on the same image.
	var sealTexts []string
	if sealPtr != nil {
		sealTexts = runSealDetection(sealPtr, *enginePtr, rgba)
	}

	return lines, conf, sealTexts, nil
}

// ── Zip helpers ──────────────────────────────────────────────────────────

func openZipFile(zr *zip.ReadCloser, name string) (io.ReadCloser, error) {
	for _, f := range zr.File {
		if f.Name == name {
			return f.Open()
		}
	}
	return nil, fmt.Errorf("%s not found in archive", name)
}
