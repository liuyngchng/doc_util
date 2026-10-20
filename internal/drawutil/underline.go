// Package drawutil provides drawing utilities for image annotation.
package drawutil

import (
	"image"
	"image/color"
)

// Red is a bright red color for hotword underlines.
var Red = color.RGBA{R: 255, G: 0, B: 0, A: 255}

// DrawUnderline draws a horizontal underline on an RGBA image.
// (x, y) is the top-left corner of the underline rectangle.
// width and thickness are in pixels.
func DrawUnderline(img *image.RGBA, x, y, width, thickness int) {
	bounds := img.Bounds()
	x0 := clamp(x, 0, bounds.Max.X)
	y0 := clamp(y, 0, bounds.Max.Y)
	x1 := clamp(x+width, 0, bounds.Max.X)
	y1 := clamp(y+thickness, 0, bounds.Max.Y)

	for py := y0; py < y1; py++ {
		rowStart := py * img.Stride
		for px := x0; px < x1; px++ {
			off := rowStart + px*4
			img.Pix[off+0] = Red.R
			img.Pix[off+1] = Red.G
			img.Pix[off+2] = Red.B
			img.Pix[off+3] = Red.A
		}
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}