//go:build noocr || !cgo

package ocr

import (
	"errors"
	"image"
)

// ── Stubs for CGO-dependent entry points ────────────────────────────────────
// These keep the ocr package compilable when CGO is disabled or the "noocr"
// build tag is set. The pure-Go files (types.go, model_config.go, box_sort.go,
// postprocess_ctc.go) provide the shared types and CheckModels/ModelsDir.

var errNoOCR = errors.New("OCR requires CGO (build with CGO_ENABLED=1 and without -tags noocr)")

// Mat is a stub for the OpenCV matrix wrapper.
type Mat struct{}

func NewMatFromRGBA(img *image.RGBA) *Mat { return nil }
func (m *Mat) Release()                    {}

// Engine is a stub for the OCR engine.
type Engine struct{}

func NewEngine(modelsDir string) (*Engine, error) { return nil, errNoOCR }
func NewEngineWithConfig(modelsDir string, cfg PaddleOCRConfig) (*Engine, error) {
	return nil, errNoOCR
}
func (e *Engine) Recognize(img *Mat) (*PageText, error)      { return nil, errNoOCR }
func (e *Engine) RecognizeWithBoxes(img *Mat) ([]TextLineWithBox, error) {
	return nil, errNoOCR
}
func (e *Engine) Release() {}

// PaddleOCRConfig is the stub config type.
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