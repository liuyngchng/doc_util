//go:build !noocr

package ocr

// DetPreprocessResult holds the preprocessed tensor and metadata.
type DetPreprocessResult struct {
	TensorData []float32
	Shape      []int64 // [1, 3, H, W]
	OriginalH  int
	OriginalW  int
}

// DetPreprocess mirrors PaddleOCR DetPreprocessor (Kotlin).
// Resizes image to a multiple of 32, normalizes with (x/255 - mean) / std.
func DetPreprocess(img *Mat, limitSideLen int, limitType string, maxSideLimit int) (*DetPreprocessResult, error) {
	originalH := img.Rows()
	originalW := img.Cols()

	// Resize to multiple of 32
	resized := resizeToMultipleOf32(img, limitSideLen, limitType, maxSideLimit)
	defer resized.Release()

	h := resized.Rows()
	w := resized.Cols()

	// Convert to CV_32FC3
	floatMat := ToFloat(resized)
	defer floatMat.Release()

	// Split channels
	ch0, ch1, ch2 := Split(floatMat)
	defer ch0.Release()
	defer ch1.Release()
	defer ch2.Release()

	// Normalize: (x/255 - mean) / std
	// mean = [0.485, 0.456, 0.406], std = [0.229, 0.224, 0.225]
	DivideScalar(ch0, 255.0)
	DivideScalar(ch1, 255.0)
	DivideScalar(ch2, 255.0)

	SubtractScalar(ch0, 0.485)
	SubtractScalar(ch1, 0.456)
	SubtractScalar(ch2, 0.406)

	DivideScalar(ch0, 0.229)
	DivideScalar(ch1, 0.224)
	DivideScalar(ch2, 0.225)

	// Pack into NCHW tensor
	channelSize := h * w
	tensorData := make([]float32, 3*channelSize)
	ch0.GetFloat(tensorData[0*channelSize : 1*channelSize])
	ch1.GetFloat(tensorData[1*channelSize : 2*channelSize])
	ch2.GetFloat(tensorData[2*channelSize : 3*channelSize])

	return &DetPreprocessResult{
		TensorData: tensorData,
		Shape:      []int64{1, 3, int64(h), int64(w)},
		OriginalH:  originalH,
		OriginalW:  originalW,
	}, nil
}

// resizeToMultipleOf32 resizes the image so that the limiting side becomes
// limitSideLen, while ensuring dimensions are multiples of 32.
func resizeToMultipleOf32(src *Mat, limitSideLen int, limitType string, maxSideLimit int) *Mat {
	h := src.Rows()
	w := src.Cols()

	ratio := 1.0
	switch limitType {
	case "max":
		if maxInt(h, w) > limitSideLen {
			ratio = float64(limitSideLen) / float64(maxInt(h, w))
		}
	case "min":
		if minInt(h, w) < limitSideLen {
			ratio = float64(limitSideLen) / float64(minInt(h, w))
		}
	default: // "min"
		if minInt(h, w) < limitSideLen {
			ratio = float64(limitSideLen) / float64(minInt(h, w))
		}
	}

	newH := int(float64(h) * ratio)
	newW := int(float64(w) * ratio)

	if maxInt(newH, newW) > maxSideLimit {
		ratio = float64(maxSideLimit) / float64(maxInt(newH, newW))
		newH = int(float64(newH) * ratio)
		newW = int(float64(newW) * ratio)
	}

	// Round to multiples of 32
	newH = maxInt(roundHalfToEven(float64(newH)/32.0)*32, 32)
	newW = maxInt(roundHalfToEven(float64(newW)/32.0)*32, 32)

	return Resize(src, newW, newH, INTER_LINEAR)
}

// roundHalfToEven implements MathUtils.roundHalfToEven from the Kotlin code.
func roundHalfToEven(v float64) int {
	floored := float64(int(v))
	diff := v - floored
	if diff < 0.5 {
		return int(floored)
	}
	if diff > 0.5 {
		return int(floored) + 1
	}
	// diff == 0.5: round to even
	iv := int(floored)
	if iv%2 == 0 {
		return iv
	}
	return iv + 1
}

// ── Seal detection preprocessing (resize_long mode) ─────────────────────────

// DetPreprocessSeal resizes the image so that the long side equals resizeLong,
// rounds dimensions up to multiples of maxStride, then normalizes with the
// same mean/std as the regular detection preprocessor.
func DetPreprocessSeal(img *Mat, resizeLong int) (*DetPreprocessResult, error) {
	originalH := img.Rows()
	originalW := img.Cols()

	resized := resizeToMultipleOf128(img, resizeLong)
	defer resized.Release()

	h := resized.Rows()
	w := resized.Cols()

	// Convert to CV_32FC3
	floatMat := ToFloat(resized)
	defer floatMat.Release()

	// Split channels
	ch0, ch1, ch2 := Split(floatMat)
	defer ch0.Release()
	defer ch1.Release()
	defer ch2.Release()

	// Normalize: (x/255 - mean) / std  (same as existing det preprocess)
	DivideScalar(ch0, 255.0)
	DivideScalar(ch1, 255.0)
	DivideScalar(ch2, 255.0)

	SubtractScalar(ch0, 0.485)
	SubtractScalar(ch1, 0.456)
	SubtractScalar(ch2, 0.406)

	DivideScalar(ch0, 0.229)
	DivideScalar(ch1, 0.224)
	DivideScalar(ch2, 0.225)

	// Pack into NCHW tensor
	channelSize := h * w
	tensorData := make([]float32, 3*channelSize)
	ch0.GetFloat(tensorData[0*channelSize : 1*channelSize])
	ch1.GetFloat(tensorData[1*channelSize : 2*channelSize])
	ch2.GetFloat(tensorData[2*channelSize : 3*channelSize])

	return &DetPreprocessResult{
		TensorData: tensorData,
		Shape:      []int64{1, 3, int64(h), int64(w)},
		OriginalH:  originalH,
		OriginalW:  originalW,
	}, nil
}

// resizeToMultipleOf128 keeps aspect ratio, scales the long side to
// resizeLong, then rounds dimensions up to multiples of 128.
// Mirrors PaddleOCR DetResizeForTest.resize_image_type2.
func resizeToMultipleOf128(src *Mat, resizeLong int) *Mat {
	h := src.Rows()
	w := src.Cols()

	resizeH := h
	resizeW := w

	if resizeH > resizeW {
		ratio := float64(resizeLong) / float64(resizeH)
		resizeH = int(float64(resizeH) * ratio)
		resizeW = int(float64(resizeW) * ratio)
	} else {
		ratio := float64(resizeLong) / float64(resizeW)
		resizeH = int(float64(resizeH) * ratio)
		resizeW = int(float64(resizeW) * ratio)
	}

	const maxStride = 128
	resizeH = ((resizeH + maxStride - 1) / maxStride) * maxStride
	resizeW = ((resizeW + maxStride - 1) / maxStride) * maxStride

	if resizeH < 32 {
		resizeH = 32
	}
	if resizeW < 32 {
		resizeW = 32
	}

	return Resize(src, resizeW, resizeH, INTER_LINEAR)
}