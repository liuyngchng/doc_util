package ocr

// PointF is a floating-point 2D point.
type PointF struct{ X, Y float64 }

// OCRBox represents a detected text bounding box with 4 corner points.
type OCRBox struct {
	Points [4]PointF // [topLeft, topRight, bottomRight, bottomLeft]
}

// TextLine holds a single line of recognized text with confidence.
type TextLine struct {
	Text       string
	Confidence float32
}

// TextLineWithBox pairs a recognized text line with its detected bounding box.
type TextLineWithBox struct {
	Text       string
	Confidence float32
	Box        OCRBox
}

// PageText holds all recognized text lines for one page.
type PageText struct {
	Lines []TextLine
}

// FullText returns all lines joined by newlines.
func (p *PageText) FullText() string {
	var s string
	for i, line := range p.Lines {
		if i > 0 {
			s += "\n"
		}
		s += line.Text
	}
	return s
}