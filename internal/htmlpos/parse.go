// Package htmlpos parses MuPDF visual HTML output to extract text with position
// information. The visual HTML format (fz_print_stext_page_as_html) produces
// <p> elements with absolute positioning (left/top in points), where each <p>
// represents one visual line of text on the page.
package htmlpos

import (
	"math"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

// TextSpan represents a block of text at a visual position on the page.
type TextSpan struct {
	Text     string
	LeftPt   float64 // x position in points
	TopPt    float64 // y position in points
	FontSize float64 // font size in points (0 if unknown)
}

// MatchResult describes a hotword found in a TextSpan, with pixel coordinates
// for drawing on a rendered image.
type MatchResult struct {
	LeftPx   int // x of underline start (pixels)
	BottomPx int // y of the underline baseline (pixels, below text)
	WidthPx  int // underline width (pixels)
}

// ParsePageHTML parses a MuPDF page HTML string and returns all text spans
// with visual positions.
func ParsePageHTML(htmlContent string) []TextSpan {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return nil
	}
	return extractSpans(doc)
}

// FindHotwordMatches searches for a hotword (case-insensitive) in spans and
// returns pixel coordinates for each occurrence. dpi is the rendering DPI
// (typically 150), used to convert pt → px.
func FindHotwordMatches(spans []TextSpan, hotword string, dpi float64) []MatchResult {
	if len(hotword) == 0 {
		return nil
	}

	ratio := dpi / 72.0
	hwLower := strings.ToLower(hotword)
	var results []MatchResult

	type charSpan struct {
		text string
		xPt  float64
	}

	// First, for each span, build a character-level position mapping.
	for _, s := range spans {
		if s.FontSize == 0 || s.Text == "" {
			continue
		}

		// Split span text into characters with estimated x positions.
		chars := make([]charSpan, 0, len([]rune(s.Text)))
		cursorPt := s.LeftPt

		for _, r := range s.Text {
			charW := charWidthPt(r, s.FontSize)
			chars = append(chars, charSpan{
				text: string(r),
				xPt:  cursorPt,
			})
			cursorPt += charW
		}

		// Flatten chars back to text for case-insensitive search.
		flatText := s.Text

		// Sliding window search.
		runes := []rune(flatText)
		hwRunes := []rune(hwLower)

		for i := 0; i <= len(runes)-len(hwRunes); i++ {
			if strings.EqualFold(string(runes[i:i+len(hwRunes)]), hwLower) && i < len(chars) {
				endIdx := i + len(hwRunes) - 1
				if endIdx >= len(chars) {
					endIdx = len(chars) - 1
				}
				leftPx := int(math.Round(chars[i].xPt * ratio))
				rightPx := int(math.Round((chars[endIdx].xPt + charWidthPt(runes[endIdx], s.FontSize)) * ratio))
				bottomPx := int(math.Round((s.TopPt + s.FontSize) * ratio))

				results = append(results, MatchResult{
					LeftPx:   leftPx,
					BottomPx: bottomPx,
					WidthPx:  rightPx - leftPx,
				})
			}
		}
	}
	return results
}

// SpanText returns the concatenated text of all spans, useful for
// checking if the page has any text content.
func SpanText(spans []TextSpan) string {
	var b strings.Builder
	for _, s := range spans {
		b.WriteString(s.Text)
	}
	return b.String()
}

// ── internal ──────────────────────────────────────────────────────────

func extractSpans(n *html.Node) []TextSpan {
	var spans []TextSpan

	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "p" {
			left, top := parsePStyle(node)
			fontSize := findFontSize(node, 10.0) // default 10pt
			text := extractText(node)
			if text != "" {
				spans = append(spans, TextSpan{
					Text:     text,
					LeftPt:   left,
					TopPt:    top,
					FontSize: fontSize,
				})
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return spans
}

// parsePStyle extracts left and top from a <p style="...left:72pt;top:100pt"> tag.
func parsePStyle(n *html.Node) (left, top float64) {
	for _, a := range n.Attr {
		if a.Key == "style" {
			left = parseStyleValue(a.Val, "left")
			top = parseStyleValue(a.Val, "top")
			return
		}
	}
	return 0, 0
}

// findFontSize recursively searches for the first span with a font-size.
func findFontSize(n *html.Node, def float64) float64 {
	var search func(*html.Node) float64
	search = func(node *html.Node) float64 {
		if node.Type == html.ElementNode && node.Data == "span" {
			for _, a := range node.Attr {
				if a.Key == "style" {
					if fs := parseStyleValue(a.Val, "font-size"); fs > 0 {
						return fs
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if rs := search(child); rs > 0 {
				return rs
			}
		}
		return 0
	}
	if rs := search(n); rs > 0 {
		return rs
	}
	return def
}

// extractText recursively extracts all text from a node, preserving
// word-separating whitespace between inline elements while stripping
// newline characters (which separate consecutive <span> tags in MuPDF
// HTML output).
func extractText(n *html.Node) string {
	var b strings.Builder

	// prevRR tracks whether the previous non-whitespace character was
	// a letter or digit.  When two text nodes are separated only by a
	// newline in the HTML we insert a single space so that words
	// don't run together.
	prevRR := false // previous char was a "real" rune
	spacePending := false

	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			for _, r := range node.Data {
				if r == '\n' || r == '\r' {
					if prevRR {
						spacePending = true
					}
					continue
				}
				if spacePending {
					b.WriteByte(' ')
					spacePending = false
				}
				b.WriteRune(r)
				if r == ' ' || r == '\t' {
					prevRR = false
				} else {
					prevRR = true
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)

	return strings.TrimSpace(b.String())
}

// parseStyleValue extracts a numeric value (removing "pt" suffix) from a CSS style string.
func parseStyleValue(style, key string) float64 {
	idx := strings.Index(style, key+":")
	if idx < 0 {
		return 0
	}
	rest := style[idx+len(key)+1:]
	end := strings.IndexByte(rest, ';')
	if end < 0 {
		end = len(rest)
	}
	val := strings.TrimSpace(rest[:end])
	val = strings.TrimSuffix(val, "pt")
	val = strings.TrimSuffix(val, "px")
	f, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return 0
	}
	return f
}

// charWidthPt estimates the width of a single character in points.
func charWidthPt(r rune, fontSize float64) float64 {
	if unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r) ||
		isFullwidth(r) {
		return fontSize * 0.95
	}
	if r == ' ' {
		return fontSize * 0.28
	}
	return fontSize * 0.55
}

// isFullwidth checks if a rune is in common fullwidth Unicode ranges.
func isFullwidth(r rune) bool {
	return (r >= 0xFF01 && r <= 0xFF60) || // Fullwidth ASCII
		(r >= 0xFFE0 && r <= 0xFFE6) || // Fullwidth symbols
		(r >= 0x3000 && r <= 0x303F) // CJK Symbols and Punctuation
}