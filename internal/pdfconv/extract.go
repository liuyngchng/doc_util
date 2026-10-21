package pdfconv

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"strings"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"

	"docutil/internal/ocr"

	"github.com/gen2brain/go-fitz"
)

// DOCX namespace URIs.
const (
	nsW   = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	nsA   = "http://schemas.openxmlformats.org/drawingml/2006/main"
	nsR   = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
)

// ExtractResult holds the text extracted from a document plus the average
// OCR confidence across the document.
type ExtractResult struct {
	Text           string  `json:"text"`
	OCRConfidence  float64 `json:"ocr_confidence"`
}

// ExtractText processes a PDF or DOCX file and returns its full text content.
// For image-only pages (scanned PDFs, DOCX embedded images), OCR is used.
// The file type is auto-detected from the magic bytes; the filename is not
// used.
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

// ── Confidence tracking ──────────────────────────────────────────────────

// confTracker accumulates text and weighted confidence for the document.
type confTracker struct {
	sb            strings.Builder
	totalChars    int
	weightedConf  float64
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
	return &ExtractResult{
		Text:          c.sb.String(),
		OCRConfidence: conf,
	}
}

// ── PDF text extraction ─────────────────────────────────────────────────

// extractPDFText extracts text from a PDF.  Each page is processed the
// same way as ModeText conversion: native MuPDF text when present,
// otherwise OCR on a 150 dpi render of the page.
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

			img, err := doc.ImageDPI(i, 150)
			if err != nil {
				return nil, fmt.Errorf("render page %d for OCR: %w", i+1, err)
			}

			ocrLines, conf := runOCRPageWithConfidence(ocrEngine, img)
			tr.addOCR(ocrLines, conf)
		}

		if i < numPages-1 {
			tr.pageSep()
		}
	}

	return tr.result(), nil
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

	// 3. Walk segments: text → addNative; image → OCR → addOCR.
	var ocrEngine *ocr.Engine
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

		ocrLines, conf, err := ocrImageFromZip(zr, mediaPath, &ocrEngine)
		if err != nil {
			// Skip images that cannot be decoded (e.g. EMF/WMF).
			continue
		}
		tr.addOCR(ocrLines, conf)
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
		// No rels file is valid — means no relationships/images.
		return nil, nil
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

// ocrImageFromZip reads an image from a zip archive, decodes it to RGBA, and
// runs OCR on it. enginePtr is a lazily-initialised *ocr.Engine pointer.
// Returns cleaned text lines and average confidence.
func ocrImageFromZip(zr *zip.ReadCloser, mediaPath string, enginePtr **ocr.Engine) ([]string, float64, error) {
	f, err := openZipFile(zr, mediaPath)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, 0, fmt.Errorf("read %s: %w", mediaPath, err)
	}

	return ocrImageBytes(data, enginePtr)
}

// ocrImageBytes decodes an image (any format supported by Go's image
// package plus BMP/TIFF/WebP via x/image) and runs OCR on it.
// Returns cleaned text lines and average confidence.
func ocrImageBytes(data []byte, enginePtr **ocr.Engine) ([]string, float64, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, fmt.Errorf("decode image: %w", err)
	}

	rgba := image.NewRGBA(src.Bounds())
	draw.Draw(rgba, rgba.Bounds(), src, src.Bounds().Min, draw.Src)

	if *enginePtr == nil {
		*enginePtr, err = initOCREngine()
		if err != nil {
			return nil, 0, fmt.Errorf("init OCR: %w", err)
		}
	}

	lines, conf := runOCRPageWithConfidence(*enginePtr, rgba)
	if len(lines) == 0 {
		return nil, 0, fmt.Errorf("OCR produced no text")
	}
	return lines, conf, nil
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