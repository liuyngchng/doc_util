package pdfconv

import (
	"archive/zip"
	"bufio"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strings"
)

// docxBuilder packs images into a minimal .docx (OOXML) file.
// Each image goes on its own page (page break between images).
type docxBuilder struct {
	images []docxImage
}

type docxImage struct {
	data   []byte // JPEG bytes
	width  int    // pixels
	height int    // pixels
}

// add adds a JPEG page.
func (b *docxBuilder) add(jpg []byte, w, h int) {
	b.images = append(b.images, docxImage{data: jpg, width: w, height: h})
}

// writeTo writes the .docx file.
func (b *docxBuilder) writeTo(w io.Writer) error {
	zw := zip.NewWriter(w)

	if err := writeFile(zw, "[Content_Types].xml", contentTypesXML); err != nil {
		return err
	}
	if err := writeFile(zw, "_rels/.rels", relsXML); err != nil {
		return err
	}

	docRels := buildDocRels(len(b.images))
	if err := writeFile(zw, "word/_rels/document.xml.rels", docRels); err != nil {
		return err
	}

	for i, img := range b.images {
		name := fmt.Sprintf("word/media/image%d.jpg", i+1)
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		if _, err := w.Write(img.data); err != nil {
			return err
		}
	}

	docXML := buildDocumentXML(b.images)
	if err := writeFile(zw, "word/document.xml", docXML); err != nil {
		return err
	}

	return zw.Close()
}

func writeFile(zw *zip.Writer, name, content string) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = w.Write([]byte(content))
	return err
}

const contentTypesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Default Extension="jpg" ContentType="image/jpeg"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`

const relsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`

func buildDocRels(n int) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n")
	b.WriteString(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` + "\n")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, `  <Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/image%d.jpg"/>`+"\n", i, i)
	}
	b.WriteString(`</Relationships>`)
	return b.String()
}

func pxToEMU(px int) int {
	return px * 9525
}

func buildDocumentXML(images []docxImage) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n")
	b.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`)
	b.WriteString(` xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"`)
	b.WriteString(` xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing"`)
	b.WriteString(` xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"`)
	b.WriteString(` xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture">` + "\n")
	b.WriteString(`<w:body>` + "\n")

	for i, img := range images {
		rID := i + 1
		cx := pxToEMU(img.width)
		cy := pxToEMU(img.height)
		maxCX := pxToEMU(718)
		maxCY := pxToEMU(1010)

		if cx > maxCX || cy > maxCY {
			scaleX := float64(maxCX) / float64(cx)
			scaleY := float64(maxCY) / float64(cy)
			scale := scaleX
			if scaleY < scaleX {
				scale = scaleY
			}
			cx = int(float64(cx) * scale)
			cy = int(float64(cy) * scale)
		}

		fmt.Fprintf(&b, `<w:p>`+"\n")
		fmt.Fprintf(&b, `  <w:r>`+"\n")
		fmt.Fprintf(&b, `    <w:drawing>`+"\n")
		fmt.Fprintf(&b, `      <wp:inline distT="0" distB="0" distL="0" distR="0">`+"\n")
		fmt.Fprintf(&b, `        <wp:extent cx="%d" cy="%d"/>`+"\n", cx, cy)
		fmt.Fprintf(&b, `        <wp:docPr id="%d" name="Image%d"/>`+"\n", rID, rID)
		fmt.Fprintf(&b, `        <a:graphic xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">`+"\n")
		fmt.Fprintf(&b, `          <a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/picture">`+"\n")
		fmt.Fprintf(&b, `            <pic:pic>`+"\n")
		fmt.Fprintf(&b, `              <pic:nvPicPr>`+"\n")
		fmt.Fprintf(&b, `                <pic:cNvPr id="%d" name="Image%d"/>`+"\n", rID, rID)
		fmt.Fprintf(&b, `                <pic:cNvPicPr/>`+"\n")
		fmt.Fprintf(&b, `              </pic:nvPicPr>`+"\n")
		fmt.Fprintf(&b, `              <pic:blipFill>`+"\n")
		fmt.Fprintf(&b, `                <a:blip r:embed="rId%d"/>`+"\n", rID)
		fmt.Fprintf(&b, `                <a:stretch><a:fillRect/></a:stretch>`+"\n")
		fmt.Fprintf(&b, `              </pic:blipFill>`+"\n")
		fmt.Fprintf(&b, `              <pic:spPr>`+"\n")
		fmt.Fprintf(&b, `                <a:xfrm><a:off x="0" y="0"/><a:ext cx="%d" cy="%d"/></a:xfrm>`+"\n", cx, cy)
		fmt.Fprintf(&b, `                <a:prstGeom prst="rect"><a:avLst/></a:prstGeom>`+"\n")
		fmt.Fprintf(&b, `              </pic:spPr>`+"\n")
		fmt.Fprintf(&b, `            </pic:pic>`+"\n")
		fmt.Fprintf(&b, `          </a:graphicData>`+"\n")
		fmt.Fprintf(&b, `        </a:graphic>`+"\n")
		fmt.Fprintf(&b, `      </wp:inline>`+"\n")
		fmt.Fprintf(&b, `    </w:drawing>`+"\n")
		fmt.Fprintf(&b, `  </w:r>`+"\n")
		fmt.Fprintf(&b, `</w:p>`+"\n")

		if i < len(images)-1 {
			b.WriteString(`<w:p><w:r><w:br w:type="page"/></w:r></w:p>` + "\n")
		}
	}

	b.WriteString(`  <w:sectPr>` + "\n")
	b.WriteString(`    <w:pgSz w:w="11906" w:h="16838"/>` + "\n")
	b.WriteString(`    <w:pgMar w:top="567" w:right="567" w:bottom="567" w:left="567"/>` + "\n")
	b.WriteString(`  </w:sectPr>` + "\n")
	b.WriteString(`</w:body>` + "\n")
	b.WriteString(`</w:document>` + "\n")
	return b.String()
}

// saveDocx saves to a file path using a bufio.Writer.
func saveDocx(path string, b *docxBuilder) error {
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

// escapeXML escapes special XML characters.
func escapeXML(s string) string {
	var b strings.Builder
	xml.Escape(&b, []byte(s))
	return b.String()
}
