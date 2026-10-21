package pdfconv

import (
	"testing"

	"docutil/internal/ocr"
)

func TestCleanOCRLine(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "CJK chars with hyphens",
			input: "- 为- 贯- 彻- 落- 实- 集- 团- 公- 司- 工- 作- 会- 议- 部- 署-",
			want:  "为贯彻落实集团公司工作会议部署",
		},
		{
			name:  "spaces between CJK chars",
			input: "为 贯 彻 落 实 集 团 公 司",
			want:  "为贯彻落实集团公司",
		},
		{
			name:  "plain CJK text (no change)",
			input: "集团公司工作会议",
			want:  "集团公司工作会议",
		},
		{
			name:  "single quote removal (model padding)",
			input: "'为'贯'彻'",
			want:  "为贯彻",
		},
		{
			name:  "Latin word hyphen preserved (no spaces)",
			input: "hello-world test",
			want:  "hello-world test",
		},
		{
			name:  "identifier A-1 preserved (no spaces)",
			input: "注: 参见附录A-1内容",
			want:  "注: 参见附录A-1内容",
		},
		// Cases from real OCR output
		{
			name:  "doc number with brackets",
			input: "公司办- 【- 2- 0- 2- 6- 】- 7- 号",
			want:  "公司办【2026】7号",
		},
		{
			name:  "date with year month day",
			input: "- 2- 0- 2- 6- 年- 9- .- 月- 2- 3- 日",
			want:  "2026年9.月23日",
		},
		{
			name:  "AI with hyphens",
			input: "A- I- 赋能比突破",
			want:  "AI赋能比突破",
		},
		{
			name:  "numbered bullet with full-width dot",
			input: "- 1- ．- 降本创效比贡献- ：- 精细",
			want:  "1．降本创效比贡献：精细",
		},
		{
			name:  "parenthesized number",
			input: "- （- 一- ）- 高度重视",
			want:  "（一）高度重视",
		},
		{
			name:  "mixed CJK and punctuation with hyphens",
			input: "为贯彻落实- ，- 深化公司- \"- 五化- \"",
			want:  "为贯彻落实，深化公司\"五化\"",
		},
		{
			name:  "page number",
			input: "- —- 1- —",
			want:  "—1—",
		},
		{
			name:  "person name",
			input: "组长- ：- 张三",
			want:  "组长：张三",
		},
		{
			name:  "trailing date (no CJK involved)",
			input: "- 3",
			want:  "3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanOCRLine(tt.input)
			if got != tt.want {
				t.Errorf("cleanOCRLine(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseHotwords(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty", "", nil},
		{"single word", "hello", []string{"hello"}},
		{"comma separated", "hello,world,test", []string{"hello", "world", "test"}},
		{"semicolon separated", "关键词1;关键词2", []string{"关键词1", "关键词2"}},
		{"Chinese comma", "北京，上海，广州", []string{"北京", "上海", "广州"}},
		{"newline", "word1\nword2\nword3", []string{"word1", "word2", "word3"}},
		{"mixed delimiters", "hello;world,北京", []string{"hello", "world", "北京"}},
		{"with spaces", " hello , world ", []string{"hello", "world"}},
		{"empty parts", "hello,,world", []string{"hello", "world"}},
		{"trailing comma", "hello,", []string{"hello"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseHotwords(tt.input)
			if len(got) != len(tt.want) {
				t.Errorf("ParseHotwords(%q) len = %d, want %d: %v", tt.input, len(got), len(tt.want), got)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("ParseHotwords(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestFindHotwordInString(t *testing.T) {
	// Create a simple OCRBox: a rectangle from (10,10) to (100,30)
	box := ocr.OCRBox{
		Points: [4]ocr.PointF{
			{X: 10, Y: 10},
			{X: 100, Y: 10},
			{X: 100, Y: 30},
			{X: 10, Y: 30},
		},
	}

	tests := []struct {
		name    string
		text    string
		hotword string
		want    int // expected number of matches
	}{
		{"exact match", "Hello World", "World", 1},
		{"no match", "Hello World", "xyz", 0},
		{"case insensitive", "HELLO", "hello", 1},
		{"Chinese", "你好世界", "世界", 1},
		{"multiple matches", "test test test", "test", 3},
		{"empty hotword", "text", "", 0},
		{"empty text", "", "test", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := findHotwordInString(tt.text, tt.hotword, box)
			if len(results) != tt.want {
				t.Errorf("findHotwordInString(%q, %q) match count = %d, want %d",
					tt.text, tt.hotword, len(results), tt.want)
			}
		})
	}
}