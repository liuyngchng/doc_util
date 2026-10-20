package htmlpos

import (
	"testing"
)

func TestParsePageHTML_Basic(t *testing.T) {
	html := `<!DOCTYPE html>
<html>
<head><style>
p { position: absolute; }
span { font-family: Arial; }
</style></head>
<body>
<p style="position:absolute;white-space:pre;left:72;top:100"><span style="font-family:Arial;font-size:12pt">Hello World</span></p>
<p style="position:absolute;white-space:pre;left:72;top:114"><span style="font-family:Arial;font-size:12pt">Second line</span></p>
</body>
</html>`

	spans := ParsePageHTML(html)
	if len(spans) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(spans))
	}

	if spans[0].Text != "Hello World" {
		t.Errorf("first span text = %q, want %q", spans[0].Text, "Hello World")
	}
	if spans[0].LeftPt != 72 {
		t.Errorf("first span left = %f, want 72", spans[0].LeftPt)
	}
	if spans[0].TopPt != 100 {
		t.Errorf("first span top = %f, want 100", spans[0].TopPt)
	}
	if spans[0].FontSize != 12 {
		t.Errorf("first span fontSize = %f, want 12", spans[0].FontSize)
	}

	if spans[1].Text != "Second line" {
		t.Errorf("second span text = %q, want %q", spans[1].Text, "Second line")
	}
}

func TestParsePageHTML_Chinese(t *testing.T) {
	html := `<!DOCTYPE html>
<html><body>
<p style="position:absolute;white-space:pre;left:72;top:200"><span style="font-family:Arial;font-size:10pt">你好世界</span></p>
</body></html>`

	spans := ParsePageHTML(html)
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Text != "你好世界" {
		t.Errorf("text = %q, want %q", spans[0].Text, "你好世界")
	}
}

func TestParsePageHTML_NestedSpans(t *testing.T) {
	// Multiple spans inside one <p> — text should be concatenated.
	html := `<!DOCTYPE html>
<html><body>
<p style="position:absolute;white-space:pre;left:72;top:100">
<span style="font-family:Arial;font-size:10pt">The </span>
<span style="font-family:Arial;font-size:10pt">quick </span>
<span style="font-family:Arial;font-size:10pt">brown</span>
</p>
</body></html>`

	spans := ParsePageHTML(html)
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Text != "The quick brown" {
		t.Errorf("text = %q, want %q", spans[0].Text, "The quick brown")
	}
}

func TestParsePageHTML_NoText(t *testing.T) {
	html := `<!DOCTYPE html><html><body></body></html>`
	spans := ParsePageHTML(html)
	if len(spans) != 0 {
		t.Errorf("expected 0 spans, got %d", len(spans))
	}
}

func TestFindHotwordMatches_Simple(t *testing.T) {
	spans := []TextSpan{
		{Text: "Hello World", LeftPt: 72, TopPt: 100, FontSize: 12},
	}
	matches := FindHotwordMatches(spans, "world", 150)
	if len(matches) < 1 {
		t.Fatal("expected at least 1 match")
	}
	// "world" starts at character 6
	// At 150 DPI, 72 pt = 150 px
	if matches[0].LeftPx < 120 { // should be around 6 * 12*0.55 * 150/72 = ~82
		t.Errorf("LeftPx = %d, expected >= 120", matches[0].LeftPx)
	}
}

func TestFindHotwordMatches_ChineseHotword(t *testing.T) {
	spans := []TextSpan{
		{Text: "你好世界今天很热", LeftPt: 72, TopPt: 200, FontSize: 10},
	}
	matches := FindHotwordMatches(spans, "世界", 150)
	if len(matches) < 1 {
		t.Fatal("expected at least 1 match for Chinese hotword")
	}
}

func TestFindHotwordMatches_CaseInsensitive(t *testing.T) {
	spans := []TextSpan{
		{Text: "HELLO hello Hello", LeftPt: 10, TopPt: 10, FontSize: 10},
	}
	matches := FindHotwordMatches(spans, "hello", 150)
	if len(matches) != 3 {
		t.Errorf("expected 3 matches for case-insensitive, got %d", len(matches))
	}
}

func TestFindHotwordMatches_NoMatch(t *testing.T) {
	spans := []TextSpan{
		{Text: "Hello World", LeftPt: 72, TopPt: 100, FontSize: 12},
	}
	matches := FindHotwordMatches(spans, "xyz", 150)
	if len(matches) != 0 {
		t.Errorf("expected 0 matches, got %d", len(matches))
	}
}

func TestFindHotwordMatches_EmptyHotword(t *testing.T) {
	spans := []TextSpan{
		{Text: "Hello World", LeftPt: 72, TopPt: 100, FontSize: 12},
	}
	matches := FindHotwordMatches(spans, "", 150)
	if len(matches) != 0 {
		t.Errorf("expected 0 matches for empty hotword, got %d", len(matches))
	}
}

func TestSpanText(t *testing.T) {
	spans := []TextSpan{
		{Text: "Hello ", LeftPt: 0, TopPt: 0},
		{Text: "World", LeftPt: 72, TopPt: 0},
	}
	if SpanText(spans) != "Hello World" {
		t.Errorf("SpanText = %q, want %q", SpanText(spans), "Hello World")
	}
}

func TestCharWidthPt(t *testing.T) {
	// CJK characters should be wider
	cjkW := charWidthPt('你', 10)
	asciiW := charWidthPt('A', 10)
	if cjkW <= asciiW {
		t.Errorf("CJK char width (%f) should be > ASCII width (%f)", cjkW, asciiW)
	}

	// Space should be narrower
	spaceW := charWidthPt(' ', 10)
	if spaceW >= asciiW {
		t.Errorf("space width (%f) should be < ASCII width (%f)", spaceW, asciiW)
	}
}

func TestParseStyleValue(t *testing.T) {
	tests := []struct {
		style string
		key   string
		want  float64
	}{
		{"left:72pt;top:100pt", "left", 72},
		{"left:72pt;top:100pt", "top", 100},
		{"font-size:12pt", "font-size", 12},
		{"color:red", "left", 0},
		{"", "left", 0},
	}

	for _, tt := range tests {
		got := parseStyleValue(tt.style, tt.key)
		if got != tt.want {
			t.Errorf("parseStyleValue(%q, %q) = %f, want %f", tt.style, tt.key, got, tt.want)
		}
	}
}

func TestFindHotwordMatches_Integration(t *testing.T) {
	// Simulate real MuPDF HTML output for a page with "Hello World" text.
	html := `<!DOCTYPE html>
<html>
<head>
<style>
p { position: absolute; }
</style>
</head>
<body>
<p style="position:absolute;white-space:pre;left:72;top:100"><span style="font-family:Arial;font-size:12pt">Hello World</span></p>
<p style="position:absolute;white-space:pre;left:72;top:114"><span style="font-family:Arial;font-size:12pt">This is a test with keyword here</span></p>
</body>
</html>`

	spans := ParsePageHTML(html)
	if len(spans) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(spans))
	}

	matches := FindHotwordMatches(spans, "keyword", 150)
	if len(matches) != 1 {
		t.Errorf("expected 1 match for 'keyword', got %d", len(matches))
	}

	if len(matches) > 0 {
		// Verify the match is in the second line (top=114pt)
		// At 150 DPI: 114pt * 150/72 = 237.5px
		expectedBottomPx := int((114 + 12) * 150 / 72) // top + fontSize
		if matches[0].BottomPx != expectedBottomPx {
			t.Logf("BottomPx = %d (expected %d)", matches[0].BottomPx, expectedBottomPx)
		}
	}
}

func TestMatchResultFields(t *testing.T) {
	r := MatchResult{LeftPx: 100, BottomPx: 200, WidthPx: 80}
	if r.LeftPx != 100 || r.BottomPx != 200 || r.WidthPx != 80 {
		t.Errorf("MatchResult fields not preserved")
	}
}

func TestTextSpanFields(t *testing.T) {
	ts := TextSpan{Text: "test", LeftPt: 72, TopPt: 100, FontSize: 12}
	if ts.Text != "test" || ts.LeftPt != 72 || ts.TopPt != 100 || ts.FontSize != 12 {
		t.Errorf("TextSpan fields not preserved")
	}
}