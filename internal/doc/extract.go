package doc

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"docutil/internal/ocr"
)

// DOCX namespace URIs.
const (
	nsW = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	nsA = "http://schemas.openxmlformats.org/drawingml/2006/main"
	nsR = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
)

// ── Public types ──────────────────────────────────────────────────────────────

// ExtractResult holds the text extracted from a document plus the average
// OCR confidence across the document.
type ExtractResult struct {
	Text          string   `json:"text"`
	OCRConfidence float64  `json:"ocr_confidence"`
	HasSeal       bool     `json:"has_seal"`
	SealText      []string `json:"seal_text"`
}

// ── File-type detection ───────────────────────────────────────────────────────

// IsDocx returns true when path is a DOCX (OOXML) file.
// Detection: reads the first 4 bytes — ZIP files start with "PK".
// For ZIP files, checks that the archive contains word/document.xml to
// distinguish DOCX from other ZIP-based formats (ODT, EPUB, etc.).
func IsDocx(path string) bool {
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

// IsOLE2File returns true when path is an OLE2 Compound Document Binary file
// (the legacy Microsoft Office container format: .doc, .xls, .ppt, etc.).
// Detection reads the 8-byte OLE2 header magic D0 CF 11 E0 A1 B1 1A E1.
func IsOLE2File(path string) bool {
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

// ── ConfTracker ───────────────────────────────────────────────────────────────

// ConfTracker accumulates text and weighted confidence for a document.
type ConfTracker struct {
	sb           strings.Builder
	totalChars   int
	weightedConf float64
	sealTexts    []string
}

// AddNative appends lines from a native text source (MuPDF text layer or DOCX
// paragraph text). Native text has confidence 1.0.
func (c *ConfTracker) AddNative(lines []string) {
	for _, l := range lines {
		c.sb.WriteString(l)
		c.sb.WriteByte('\n')
		chars := len([]rune(l))
		c.weightedConf += 1.0 * float64(chars)
		c.totalChars += chars
	}
}

// AddOCR appends OCR lines with the given per-line confidence.
func (c *ConfTracker) AddOCR(lines []string, conf float64) {
	for _, l := range lines {
		c.sb.WriteString(l)
		c.sb.WriteByte('\n')
		chars := len([]rune(l))
		c.weightedConf += conf * float64(chars)
		c.totalChars += chars
	}
}

// AddSeal appends seal texts found on the page.
func (c *ConfTracker) AddSeal(texts []string) {
	c.sealTexts = append(c.sealTexts, texts...)
}

// PageSep adds a blank line between pages (no confidence contribution).
func (c *ConfTracker) PageSep() {
	// Only add separator when there is already content.
	if c.totalChars > 0 {
		c.sb.WriteByte('\n')
	}
}

// Result returns the accumulated extraction result.
func (c *ConfTracker) Result() *ExtractResult {
	conf := 1.0
	if c.totalChars > 0 {
		conf = c.weightedConf / float64(c.totalChars)
	}
	sealTexts := c.sealTexts
	if sealTexts == nil {
		sealTexts = []string{}
	}
	hasSeal := len(c.sealTexts) > 0
	return &ExtractResult{
		Text:          c.sb.String(),
		OCRConfidence: conf,
		HasSeal:       hasSeal,
		SealText:      sealTexts,
	}
}

// ── DOCX text extraction ──────────────────────────────────────────────────────

// DocxSegment represents a content segment inside word/document.xml.
// A segment is either a text paragraph or an embedded image reference.
type DocxSegment struct {
	Text  string // paragraph text (may be empty for image-only runs)
	Image string // rId of the image in the .rels file, e.g. "rId5"
}

// ExtractDocxText extracts text from a DOCX by reading the main document
// body and OCR-ing any embedded images in document order.
func ExtractDocxText(path string) (*ExtractResult, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open DOCX: %w", err)
	}
	defer zr.Close()

	// 1. Parse word/_rels/document.xml.rels → rId → media path.
	relTargets, err := ParseRels(zr)
	if err != nil {
		return nil, fmt.Errorf("parse rels: %w", err)
	}

	// 2. Read word/document.xml and collect segments in document order.
	docFile, err := OpenZipFile(zr, "word/document.xml")
	if err != nil {
		return nil, fmt.Errorf("open document.xml: %w", err)
	}
	segments, err := ParseDocumentXML(docFile)
	docFile.Close()
	if err != nil {
		return nil, fmt.Errorf("parse document.xml: %w", err)
	}

	// 3. Walk segments: text → AddNative; image → OCR + seal → AddOCR.
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
	tr := &ConfTracker{}

	for _, seg := range segments {
		if seg.Text != "" {
			tr.AddNative([]string{seg.Text})
			continue
		}

		// Image segment.
		if seg.Image == "" {
			continue
		}

		mediaPath, ok := relTargets[seg.Image]
		if !ok {
			continue
		}

		ocrLines, conf, sealTexts, err := ocrImageWithSealFromZip(zr, mediaPath, &ocrEngine, &sealDetector)
		if err != nil {
			// Skip images that cannot be decoded (e.g. EMF/WMF)
			// or produced no text and no seals.
			continue
		}
		tr.AddOCR(ocrLines, conf)
		if len(sealTexts) > 0 {
			tr.AddSeal(sealTexts)
		}
	}

	return tr.Result(), nil
}

// ── DOCX parsing helpers ──────────────────────────────────────────────────────

// ParseRels reads word/_rels/document.xml.rels and returns a map from rId
// (e.g. "rId5") to the target path (e.g. "media/image1.png").
func ParseRels(zr *zip.ReadCloser) (map[string]string, error) {
	const relsFile = "word/_rels/document.xml.rels"

	f, err := OpenZipFile(zr, relsFile)
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

// ParseDocumentXML extracts text and image references from a DOCX
// document.xml body, preserving document order.
//
// It walks the XML tree with xml.Decoder, collecting <w:t> text runs within
// each <w:p> paragraph and detecting <a:blip> elements whose r:embed
// attribute references an image.  Each paragraph becomes one segment: text,
// an image, or (rarely) both when a paragraph mixes runs and a drawing.
func ParseDocumentXML(r io.Reader) ([]DocxSegment, error) {
	decoder := xml.NewDecoder(r)

	var segments []DocxSegment
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
					FlushParagraph(&segments, &curText, lastImageRID)
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

// FlushParagraph appends the accumulated paragraph content as one or two
// segments (text first, then image) and clears the accumulator.
func FlushParagraph(segments *[]DocxSegment, curText *strings.Builder, imageRID string) {
	if curText.Len() > 0 {
		*segments = append(*segments, DocxSegment{Text: curText.String()})
		curText.Reset()
	}
	if imageRID != "" {
		*segments = append(*segments, DocxSegment{Image: imageRID})
	}
}

// ── Legacy .doc (OLE2) conversion ─────────────────────────────────────────────

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

// ConvertOLE2ToDocx converts a legacy Office file (.doc) to .docx using
// LibreOffice headless. It returns the path to the generated .docx file and a
// cleanup function that removes both the file and its containing temp directory.
func ConvertOLE2ToDocx(srcPath string) (docxPath string, cleanup func(), _ error) {
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

// ExtractOLE2Text converts a legacy Office file (.doc) to .docx via
// LibreOffice headless, then extracts text from the result.  All temporary files
// are cleaned up before return.
func ExtractOLE2Text(path string) (*ExtractResult, error) {
	docxTmp, cleanup, err := ConvertOLE2ToDocx(path)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	return ExtractDocxText(docxTmp)
}

// ── Zip helpers ───────────────────────────────────────────────────────────────

// OpenZipFile opens a named file from a zip archive.
func OpenZipFile(zr *zip.ReadCloser, name string) (io.ReadCloser, error) {
	for _, f := range zr.File {
		if f.Name == name {
			return f.Open()
		}
	}
	return nil, fmt.Errorf("%s not found in archive", name)
}

// ── Image OCR helper ──────────────────────────────────────────────────────────

// ocrImageWithSealFromZip reads an image from a zip archive, decodes it to
// RGBA, runs OCR and seal detection on it. enginePtr and sealPtr are
// lazily-initialised. Returns cleaned text lines, average confidence, and
// any seal texts detected.
func ocrImageWithSealFromZip(zr *zip.ReadCloser, mediaPath string, enginePtr **ocr.Engine, sealPtr **ocr.SealDetector) ([]string, float64, []string, error) {
	f, err := OpenZipFile(zr, mediaPath)
	if err != nil {
		return nil, 0, nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("read %s: %w", mediaPath, err)
	}

	return OCRImageBytesWithSeal(data, enginePtr, sealPtr)
}