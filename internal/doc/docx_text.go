package doc

import (
	"archive/zip"
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// TextDocxBuilder packs recognized text into a minimal .docx with
// one paragraph per text line and a page break between pages.
type TextDocxBuilder struct {
	pages [][]string // each page is a list of text lines
}

// AddPage appends a page of recognized text lines.
func (b *TextDocxBuilder) AddPage(lines []string) {
	b.pages = append(b.pages, lines)
}

func (b *TextDocxBuilder) writeTo(w io.Writer) error {
	zw := zip.NewWriter(w)

	if err := writeFile(zw, "[Content_Types].xml", textContentTypesXML); err != nil {
		return err
	}
	if err := writeFile(zw, "_rels/.rels", textRelsXML); err != nil {
		return err
	}
	if err := writeFile(zw, "word/_rels/document.xml.rels", textDocRelsXML); err != nil {
		return err
	}
	if err := writeFile(zw, "word/document.xml", b.buildDocumentXML()); err != nil {
		return err
	}
	return zw.Close()
}

func (b *TextDocxBuilder) buildDocumentXML() string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n")
	sb.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` + "\n")
	sb.WriteString(`<w:body>` + "\n")

	for pi, page := range b.pages {
		if len(page) == 0 {
			// Empty page: still emit a blank paragraph + page break
			sb.WriteString(`  <w:p><w:r><w:t xml:space="preserve"></w:t></w:r></w:p>` + "\n")
		}
		for _, line := range page {
			sb.WriteString(`  <w:p>` + "\n")
			sb.WriteString(`    <w:r>` + "\n")
			sb.WriteString(`      <w:t xml:space="preserve">` + escapeXML(line) + `</w:t>` + "\n")
			sb.WriteString(`    </w:r>` + "\n")
			sb.WriteString(`  </w:p>` + "\n")
		}
		if pi < len(b.pages)-1 {
			sb.WriteString(`  <w:p><w:r><w:br w:type="page"/></w:r></w:p>` + "\n")
		}
	}

	sb.WriteString(`  <w:sectPr>` + "\n")
	sb.WriteString(`    <w:pgSz w:w="11906" w:h="16838"/>` + "\n")
	sb.WriteString(`    <w:pgMar w:top="1134" w:right="1134" w:bottom="1134" w:left="1134"/>` + "\n")
	sb.WriteString(`  </w:sectPr>` + "\n")
	sb.WriteString(`</w:body>` + "\n")
	sb.WriteString(`</w:document>` + "\n")
	return sb.String()
}

const textContentTypesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`

const textRelsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`

const textDocRelsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
</Relationships>`

// SaveTextDocx saves the text builder to a file.
func SaveTextDocx(path string, b *TextDocxBuilder) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create docx: %w", err)
	}

	bw := bufio.NewWriter(f)
	if err := b.writeTo(bw); err != nil {
		f.Close()
		return fmt.Errorf("write docx: %w", err)
	}
	if err := bw.Flush(); err != nil {
		f.Close()
		return fmt.Errorf("flush docx: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close docx: %w", err)
	}
	return nil
}
