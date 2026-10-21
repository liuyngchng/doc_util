package ocr

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ModelConfig holds the character list parsed from inference.yml.
type ModelConfig struct {
	CharacterList []string
}

// ParseModelConfig reads a PaddleOCR inference.yml and extracts the character list.
func ParseModelConfig(ymlPath string) (*ModelConfig, error) {
	f, err := os.Open(ymlPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", ymlPath, err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", ymlPath, err)
	}

	// Find "PostProcess:" section
	postProcessLine := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "PostProcess:" {
			postProcessLine = i
			break
		}
	}
	if postProcessLine < 0 {
		return nil, fmt.Errorf("missing PostProcess section in %s", ymlPath)
	}

	postIndent := leadingSpaces(lines[postProcessLine])

	// Find "character_dict:" inside PostProcess
	dictLine := -1
	for i := postProcessLine + 1; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := leadingSpaces(line)
		if indent <= postIndent {
			break
		}
		if trimmed == "character_dict:" {
			dictLine = i
			break
		}
	}
	if dictLine < 0 {
		return nil, fmt.Errorf("missing character_dict in %s", ymlPath)
	}

	dictIndent := leadingSpaces(lines[dictLine])
	var chars []string
	for i := dictLine + 1; i < len(lines); i++ {
		line := lines[i]
		indent := leadingSpaces(line)
		content := line[indent:]
		// Skip empty lines and comments.
		if content == "" || strings.HasPrefix(content, "#") {
			continue
		}
		if !strings.HasPrefix(content, "-") {
			if indent <= dictIndent {
				break
			}
			continue
		}
		// Strip the YAML list marker "- " (dash + optional spaces).
		valueStr := strings.TrimPrefix(content, "-")
		valueStr = strings.TrimLeft(valueStr, " ")
		char := parseYamlListScalar(valueStr)
		chars = append(chars, char)
	}

	if len(chars) == 0 {
		return nil, fmt.Errorf("empty character_dict in %s", ymlPath)
	}

	// Append space if not present (matching Kotlin behavior)
	if chars[len(chars)-1] != " " {
		chars = append(chars, " ")
	}

	return &ModelConfig{CharacterList: chars}, nil
}

func leadingSpaces(s string) int {
	n := 0
	for _, ch := range s {
		if ch == ' ' {
			n++
		} else {
			break
		}
	}
	return n
}

func parseYamlListScalar(s string) string {
	// Trim only YAML whitespace (space U+0020 and tab U+0009).  Unlike
	// strings.TrimSpace, this preserves Unicode whitespace such as the
	// full-width space U+3000 that PP-OCRv5 uses as a valid dictionary
	// character.
	s = strings.Trim(s, " \t")
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'")
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		val := s[1 : len(s)-1]
		val = strings.ReplaceAll(val, "\\\"", "\"")
		val = strings.ReplaceAll(val, "\\\\", "\\")
		val = strings.ReplaceAll(val, "\\n", "\n")
		val = strings.ReplaceAll(val, "\\r", "\r")
		val = strings.ReplaceAll(val, "\\t", "\t")
		return val
	}
	return s
}

// CheckModels returns true if all required model files exist.
func CheckModels(baseDir string) bool {
	det := filepath.Join(baseDir, "models", "det", "inference.onnx")
	rec := filepath.Join(baseDir, "models", "rec", "inference.onnx")
	yml := filepath.Join(baseDir, "models", "rec", "inference.yml")
	_, err1 := os.Stat(det)
	_, err2 := os.Stat(rec)
	_, err3 := os.Stat(yml)
	return err1 == nil && err2 == nil && err3 == nil
}

// CheckAllModels returns nil when all required models exist (detection,
// recognition and seal detection). It returns an error naming the first
// missing file otherwise.
func CheckAllModels(baseDir string) error {
	required := []string{
		filepath.Join(baseDir, "models", "det", "inference.onnx"),
		filepath.Join(baseDir, "models", "rec", "inference.onnx"),
		filepath.Join(baseDir, "models", "rec", "inference.yml"),
		filepath.Join(baseDir, "models", "seal_det", "inference.onnx"),
	}
	for _, p := range required {
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("required model missing: %s", p)
		}
	}
	return nil
}

// CheckSealModel returns true if the seal detection model file exists.
func CheckSealModel(baseDir string) bool {
	seal := filepath.Join(baseDir, "models", "seal_det", "inference.onnx")
	_, err := os.Stat(seal)
	return err == nil
}

// SealModelPath returns the path to the seal detection ONNX model.
func SealModelPath(baseDir string) string {
	return filepath.Join(baseDir, "models", "seal_det", "inference.onnx")
}

// ModelsDir returns the models directory path.
func ModelsDir(baseDir string) string {
	return filepath.Join(baseDir, "models")
}