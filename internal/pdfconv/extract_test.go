package pdfconv

import (
	"strings"
	"testing"
)

func TestConfTrackerNativeOnly(t *testing.T) {
	tr := &confTracker{}
	tr.addNative([]string{"hello", "世界"})
	r := tr.result()

	if r.Text != "hello\n世界\n" {
		t.Fatalf("text = %q, want %q", r.Text, "hello\n世界\n")
	}
	if r.OCRConfidence != 1.0 {
		t.Fatalf("confidence = %f, want 1.0", r.OCRConfidence)
	}
}

func TestConfTrackerOCROnly(t *testing.T) {
	tr := &confTracker{}
	tr.addOCR([]string{"abc"}, 0.8)
	tr.addOCR([]string{"defg"}, 0.6)
	r := tr.result()

	if r.Text != "abc\ndefg\n" {
		t.Fatalf("text = %q", r.Text)
	}

	// char-weighted: "abc"=3*0.8 + "defg"=4*0.6 → (2.4+2.4)/7 = 0.6857...
	expected := (3*0.8 + 4*0.6) / 7.0
	if r.OCRConfidence < expected-0.001 || r.OCRConfidence > expected+0.001 {
		t.Fatalf("confidence = %f, want ~%f", r.OCRConfidence, expected)
	}
}

func TestConfTrackerMixed(t *testing.T) {
	tr := &confTracker{}
	tr.addNative([]string{"原生文本"})   // 4 chars, conf=1.0
	tr.addOCR([]string{"abc"}, 0.9)     // 3 chars, conf=0.9
	r := tr.result()

	// weighted: 4*1.0 + 3*0.9 = 6.7 / 7 = 0.9571...
	expected := (4*1.0 + 3*0.9) / 7.0
	if r.OCRConfidence < expected-0.001 || r.OCRConfidence > expected+0.001 {
		t.Fatalf("confidence = %f, want ~%f", r.OCRConfidence, expected)
	}
}

func TestConfTrackerEmpty(t *testing.T) {
	tr := &confTracker{}
	r := tr.result()
	if r.Text != "" {
		t.Fatalf("text should be empty, got %q", r.Text)
	}
	if r.OCRConfidence != 1.0 {
		t.Fatalf("confidence should be 1.0 for empty doc, got %f", r.OCRConfidence)
	}
}

func TestParseDocumentXMLTextOnly(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:r><w:t>第一段</w:t></w:r></w:p>
    <w:p><w:r><w:t>第二段</w:t></w:r></w:p>
  </w:body>
</w:document>`

	segs, err := parseDocumentXML(strings.NewReader(xml))
	if err != nil {
		t.Fatalf("parseDocumentXML: %v", err)
	}

	want := []docxSegment{
		{text: "第一段"},
		{text: "第二段"},
	}
	assertSegments(t, segs, want)
}

func TestParseDocumentXMLImageOnly(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"
  xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
  xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <w:body>
    <w:p>
      <w:r>
        <w:drawing>
          <a:blip r:embed="rId4"/>
        </w:drawing>
      </w:r>
    </w:p>
  </w:body>
</w:document>`

	segs, err := parseDocumentXML(strings.NewReader(xml))
	if err != nil {
		t.Fatalf("parseDocumentXML: %v", err)
	}

	want := []docxSegment{
		{image: "rId4"},
	}
	assertSegments(t, segs, want)
}

func TestParseDocumentXMLMixedOrder(t *testing.T) {
	// A paragraph of text, then an image, then more text — document order must
	// be preserved.
	xml := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"
  xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
  xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <w:body>
    <w:p><w:r><w:t>前文</w:t></w:r></w:p>
    <w:p><w:r><w:drawing><a:blip r:embed="rId7"/></w:drawing></w:r></w:p>
    <w:p><w:r><w:t>后文</w:t></w:r></w:p>
  </w:body>
</w:document>`

	segs, err := parseDocumentXML(strings.NewReader(xml))
	if err != nil {
		t.Fatalf("parseDocumentXML: %v", err)
	}

	want := []docxSegment{
		{text: "前文"},
		{image: "rId7"},
		{text: "后文"},
	}
	assertSegments(t, segs, want)
}

func TestParseDocumentXMLMultipleRuns(t *testing.T) {
	// Multiple <w:t> runs within one paragraph are concatenated in order.
	xml := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p>
      <w:r><w:t>甲</w:t></w:r>
      <w:r><w:t>乙</w:t></w:r>
      <w:r><w:t>丙</w:t></w:r>
    </w:p>
  </w:body>
</w:document>`

	segs, err := parseDocumentXML(strings.NewReader(xml))
	if err != nil {
		t.Fatalf("parseDocumentXML: %v", err)
	}

	want := []docxSegment{
		{text: "甲乙丙"},
	}
	assertSegments(t, segs, want)
}

func TestParseDocumentXMLLineBreak(t *testing.T) {
	// <w:br/> inside a run becomes a newline.
	xml := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p>
      <w:r><w:t>第一行</w:t><w:br/><w:t>第二行</w:t></w:r>
    </w:p>
  </w:body>
</w:document>`

	segs, err := parseDocumentXML(strings.NewReader(xml))
	if err != nil {
		t.Fatalf("parseDocumentXML: %v", err)
	}

	want := []docxSegment{
		{text: "第一行\n第二行"},
	}
	assertSegments(t, segs, want)
}

func assertSegments(t *testing.T, got, want []docxSegment) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("segment count = %d, want %d:\n got: %+v\nwant: %+v", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("segment[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
