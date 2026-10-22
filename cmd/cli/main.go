// Command cli is a headless PDF → Word converter used for testing and
// validating the pure-Go rendering pipeline without the Fyne GUI.
//
// Build (requires CGO for OCR support):
//
//	CGO_ENABLED=1 go build -o docutil-cli ./cmd/cli/
//
// Or without OCR:
//
//	CGO_ENABLED=0 go build -tags noocr -o docutil-cli ./cmd/cli/
package main

import (
	"fmt"
	"os"

	"docutil/internal/doc"
	"docutil/internal/pdf"
)

func main() {
	mode := doc.ModeImage
	var pdfPath string
	var hotwordsArg string

	for _, arg := range os.Args[1:] {
		switch arg {
		case "--ocr":
			// Backward compatible alias for --mode text.
			mode = doc.ModeText
		case "--mode":
			// Consumed below via positional check.
		case "--hotwords":
			// Consumed below via positional check.
		default:
			pdfPath = arg
		}
	}

	// Check for --mode <value> and --hotwords <value> in args.
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "--mode" && i+1 < len(os.Args) {
			switch os.Args[i+1] {
			case "image":
				mode = doc.ModeImage
			case "text":
				mode = doc.ModeText
			default:
				fmt.Fprintf(os.Stderr, "Unknown mode: %s (valid: image, text)\n", os.Args[i+1])
				os.Exit(1)
			}
		}
		if os.Args[i] == "--hotwords" && i+1 < len(os.Args) {
			hotwordsArg = os.Args[i+1]
		}
	}

	if pdfPath == "" {
		fmt.Fprintf(os.Stderr, "Usage: docutil-cli [--mode image|text] [--hotwords <w1,w2,...>] [--ocr] <pdf_file>\n")
		os.Exit(1)
	}

	hotwords := doc.ParseHotwords(hotwordsArg)

	modeLabel := "image"
	if mode == doc.ModeText {
		modeLabel = "text"
	}
	fmt.Printf("Converting: %s (mode: %s)", pdfPath, modeLabel)
	if len(hotwords) > 0 {
		fmt.Printf(", hotwords: %v", hotwords)
	}
	fmt.Println()

	var docxPath string
	var err error
	if len(hotwords) > 0 && mode == doc.ModeImage {
		docxPath, err = pdf.ConvertWithHotwords(pdfPath, mode, hotwords, func(pct float64) {
			showProgress(pct)
		})
	} else {
		docxPath, err = pdf.Convert(pdfPath, mode, func(pct float64) {
			showProgress(pct)
		})
	}
	fmt.Println()

	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Done: %s\n", docxPath)
}

func showProgress(pct float64) {
	const barWidth = 40
	filled := int(pct * float64(barWidth))
	bar := ""
	for i := 0; i < barWidth; i++ {
		if i < filled {
			bar += "="
		} else {
			bar += "-"
		}
	}
	fmt.Printf("\r[%s] %.0f%%", bar, pct*100)
}