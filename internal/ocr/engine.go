//go:build !noocr

package ocr

import (
	"fmt"
	"os"
	"path/filepath"
)

// Engine runs PaddleOCR detection + recognition on page images.
type Engine struct {
	detSession    *ORTSession
	recSession    *ORTSession
	characterList []string
	detConfig     PaddleOCRConfig
}

// PaddleOCRConfig holds runtime parameters for the OCR pipeline.
type PaddleOCRConfig struct {
	DetLimitSideLen  int
	DetLimitType     string
	DetMaxSideLimit  int
	DetThresh        float64
	DetBoxThresh     float64
	DetUnclipRatio   float64
	DetMaxCandidates int
	DetUseDilation   bool
	RecScoreThresh   float32
}

// DefaultConfig returns PaddleOCR defaults.
func DefaultConfig() PaddleOCRConfig {
	return PaddleOCRConfig{
		DetLimitSideLen:  64,
		DetLimitType:     "min",
		DetMaxSideLimit:  4000,
		DetThresh:        0.3,
		DetBoxThresh:     0.6,
		DetUnclipRatio:   1.5,
		DetMaxCandidates: 3000,
		DetUseDilation:   false,
		RecScoreThresh:   0.0,
	}
}

// NewEngine loads the detection and recognition models from the models directory.
func NewEngine(modelsDir string) (*Engine, error) {
	return NewEngineWithConfig(modelsDir, DefaultConfig())
}

// NewEngineWithConfig creates an engine with explicit config.
func NewEngineWithConfig(modelsDir string, cfg PaddleOCRConfig) (*Engine, error) {
	detPath := filepath.Join(modelsDir, "det", "inference.onnx")
	recPath := filepath.Join(modelsDir, "rec", "inference.onnx")
	ymlPath := filepath.Join(modelsDir, "rec", "inference.yml")

	// Load model config (character list)
	mcfg, err := ParseModelConfig(ymlPath)
	if err != nil {
		return nil, fmt.Errorf("parse model config: %w", err)
	}

	// Load detection model
	detBytes, err := os.ReadFile(detPath)
	if err != nil {
		return nil, fmt.Errorf("read det model: %w", err)
	}
	detSession, err := NewORTSession(detBytes, "x")
	if err != nil {
		return nil, fmt.Errorf("load det model: %w", err)
	}

	// Load recognition model
	recBytes, err := os.ReadFile(recPath)
	if err != nil {
		detSession.Release()
		return nil, fmt.Errorf("read rec model: %w", err)
	}
	recSession, err := NewORTSession(recBytes, "x")
	if err != nil {
		detSession.Release()
		return nil, fmt.Errorf("load rec model: %w", err)
	}

	return &Engine{
		detSession:    detSession,
		recSession:    recSession,
		characterList: mcfg.CharacterList,
		detConfig:     cfg,
	}, nil
}

// Release frees all resources.
func (e *Engine) Release() {
	if e.detSession != nil {
		e.detSession.Release()
	}
	if e.recSession != nil {
		e.recSession.Release()
	}
}

// Recognize runs the full OCR pipeline on a page image (RGBA) and returns
// recognized text lines (without bounding boxes).
func (e *Engine) Recognize(img *Mat) (*PageText, error) {
	lines, err := e.RecognizeWithBoxes(img)
	if err != nil {
		return nil, err
	}

	var textLines []TextLine
	for _, l := range lines {
		textLines = append(textLines, TextLine{Text: l.Text, Confidence: l.Confidence})
	}
	return &PageText{Lines: textLines}, nil
}

// RecognizeWithBoxes runs the full OCR pipeline and returns each recognized
// line paired with its detected bounding box (in original image pixels).
func (e *Engine) RecognizeWithBoxes(img *Mat) ([]TextLineWithBox, error) {
	// 1. Detection
	detResult, err := DetPreprocess(
		img,
		e.detConfig.DetLimitSideLen,
		e.detConfig.DetLimitType,
		e.detConfig.DetMaxSideLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("det preprocess: %w", err)
	}

	detOut, detShape, err := e.detSession.Run(detResult.TensorData, detResult.Shape)
	if err != nil {
		return nil, fmt.Errorf("det inference: %w", err)
	}

	// 2. DB postprocess
	post := DBPostProcessor{
		Thresh:        e.detConfig.DetThresh,
		BoxThresh:     e.detConfig.DetBoxThresh,
		UnclipRatio:   e.detConfig.DetUnclipRatio,
		MaxCandidates: e.detConfig.DetMaxCandidates,
		UseDilation:   e.detConfig.DetUseDilation,
	}
	boxes, err := post.Process(detOut, detShape, detResult.OriginalW, detResult.OriginalH)
	if err != nil {
		return nil, fmt.Errorf("det postprocess: %w", err)
	}

	if len(boxes) == 0 {
		return nil, nil
	}

	// 3. Sort boxes
	sortedBoxes := SortBoxesInReadingOrder(boxes)

	// 4. Crop and recognize
	var allLines []TextLineWithBox
	for _, box := range sortedBoxes {
		crop := cropBox(img, box)
		if crop == nil {
			continue
		}

		recResult, err := RecPreprocessBatch([]*Mat{crop})
		if err != nil {
			crop.Release()
			return nil, fmt.Errorf("rec preprocess: %w", err)
		}
		crop.Release()

		recOut, recShape, err := e.recSession.Run(recResult.TensorData, recResult.Shape)
		if err != nil {
			return nil, fmt.Errorf("rec inference: %w", err)
		}

		lines := CTCDecode(recOut, recShape, e.characterList)
		for _, line := range lines {
			if line.Confidence >= e.detConfig.RecScoreThresh {
				allLines = append(allLines, TextLineWithBox{
					Text:       line.Text,
					Confidence: line.Confidence,
					Box:        box,
				})
			}
		}
	}

	return allLines, nil
}

// cropBox crops a perspective-corrected region from the image for recognition.
func cropBox(src *Mat, box OCRBox) *Mat {
	// Recompute minAreaRect from the detected quad, matching QuadTextCrop.
	rectInput := make([]ContourPoint, 4)
	for i := 0; i < 4; i++ {
		rectInput[i] = ContourPoint{roundInt(box.Points[i].X), roundInt(box.Points[i].Y)}
	}
	boundingBox := MinAreaRect(rectInput)
	ordered := orderMinAreaRectPoints(boundingBox)

	widthTop := hypot(ordered[0].X-ordered[1].X, ordered[0].Y-ordered[1].Y)
	widthBottom := hypot(ordered[2].X-ordered[3].X, ordered[2].Y-ordered[3].Y)
	heightLeft := hypot(ordered[0].X-ordered[3].X, ordered[0].Y-ordered[3].Y)
	heightRight := hypot(ordered[1].X-ordered[2].X, ordered[1].Y-ordered[2].Y)

	dstW := maxInt(int(maxFloat64(widthTop, widthBottom)), 1)
	dstH := maxInt(int(maxFloat64(heightLeft, heightRight)), 1)

	srcPts := [4]PointF{ordered[0], ordered[1], ordered[2], ordered[3]}
	dstPts := [4]PointF{
		{X: 0, Y: 0},
		{X: float64(dstW), Y: 0},
		{X: float64(dstW), Y: float64(dstH)},
		{X: 0, Y: float64(dstH)},
	}

	transform := GetPerspectiveTransform(srcPts, dstPts)
	if transform == nil {
		return nil
	}
	defer transform.Release()

	dst := WarpPerspective(src, transform, dstW, dstH, BORDER_REPLICATE)
	if dst == nil {
		return nil
	}

	// Vertical crop rotation (matching QuadTextCrop)
	if float64(dst.Rows())/float64(dst.Cols()) >= 1.5 {
		rotated := Rotate90CCW(dst)
		dst.Release()
		return rotated
	}
	return dst
}