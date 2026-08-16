package md

import (
	"fmt"
	"testing"
)

// mockBuffer 实现 BufferReader 接口
type mockBuffer struct {
	lines []string
}

func (m *mockBuffer) LinesNum() int { return len(m.lines) }
func (m *mockBuffer) LineBytes(n int) []byte {
	if n >= 0 && n < len(m.lines) {
		return []byte(m.lines[n])
	}
	return nil
}

// segmentType 用于标识 segment 的渲染类型（测试用）
type segmentType int

const (
	typeUnknown segmentType = iota
	typeHeading
	typeCodeBlock
	typeTable
	typeBlockquote
	typeList
	typeHR
	typeNormal
)

// getSegmentType 通过检查 segment 的 Render 函数指针来识别其类型
func getSegmentType(seg Segment) segmentType {
	if seg.Render == nil {
		return typeUnknown
	}
	switch fmt.Sprintf("%p", seg.Render) {
	case fmt.Sprintf("%p", RenderHeading):
		return typeHeading
	case fmt.Sprintf("%p", RenderCodeBlock):
		return typeCodeBlock
	case fmt.Sprintf("%p", RenderTable):
		return typeTable
	case fmt.Sprintf("%p", RenderBlockquote):
		return typeBlockquote
	case fmt.Sprintf("%p", RenderList):
		return typeList
	case fmt.Sprintf("%p", RenderHR):
		return typeHR
	default:
		return typeNormal
	}
}

func TestDetectHeading(t *testing.T) {
	buf := &mockBuffer{lines: []string{
		"# Title",
		"normal text",
	}}
	segments := DetectSegments(buf, 0, 1)
	if len(segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segments))
	}
	if segments[0].BufStartLine != 0 || segments[0].BufEndLine != 0 {
		t.Fatalf("heading segment: expected [0,0], got [%d,%d]",
			segments[0].BufStartLine, segments[0].BufEndLine)
	}
	if segments[1].BufStartLine != 1 || segments[1].BufEndLine != 1 {
		t.Fatalf("normal segment: expected [1,1], got [%d,%d]",
			segments[1].BufStartLine, segments[1].BufEndLine)
	}
}

func TestDetectMultipleHeadings(t *testing.T) {
	buf := &mockBuffer{lines: []string{
		"# Title",
		"## Subtitle",
		"### Sub-subtitle",
		"# Another Title",
	}}
	segments := DetectSegments(buf, 0, 3)
	if len(segments) != 4 {
		t.Fatalf("expected 4 segments, got %d", len(segments))
	}
	for i := range segments {
		segType := getSegmentType(segments[i])
		if segType != typeHeading {
			t.Fatalf("segment %d: expected heading, got %v", i, segType)
		}
	}
}

func TestDetectCodeBlock(t *testing.T) {
	buf := &mockBuffer{lines: []string{
		"```",
		"code line 1",
		"code line 2",
		"```",
		"after code",
	}}
	segments := DetectSegments(buf, 0, 4)
	if len(segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segments))
	}
	if segments[0].BufStartLine != 0 || segments[0].BufEndLine != 3 {
		t.Fatalf("codeblock segment: expected [0,3], got [%d,%d]",
			segments[0].BufStartLine, segments[0].BufEndLine)
	}
	if segType := getSegmentType(segments[0]); segType != typeCodeBlock {
		t.Fatalf("expected codeblock, got %v", segType)
	}
	if segType := getSegmentType(segments[1]); segType != typeNormal {
		t.Fatalf("expected normal, got %v", segType)
	}
}

func TestDetectCodeBlockWithTilde(t *testing.T) {
	buf := &mockBuffer{lines: []string{
		"~~~",
		"code",
		"~~~",
	}}
	segments := DetectSegments(buf, 0, 2)
	if len(segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segments))
	}
	if segType := getSegmentType(segments[0]); segType != typeCodeBlock {
		t.Fatalf("expected codeblock, got %v", segType)
	}
}

func TestDetectUnclosedCodeBlock(t *testing.T) {
	buf := &mockBuffer{lines: []string{
		"```",
		"code without closing",
	}}
	segments := DetectSegments(buf, 0, 1)
	if len(segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segments))
	}
	if segType := getSegmentType(segments[0]); segType != typeCodeBlock {
		t.Fatalf("expected codeblock, got %v", segType)
	}
	if segments[0].BufEndLine != 1 {
		t.Fatalf("codeblock end: expected 1, got %d", segments[0].BufEndLine)
	}
}

func TestDetectTable(t *testing.T) {
	buf := &mockBuffer{lines: []string{
		"| col A | col B |",
		"|-------|-------|",
		"| data1 | data2 |",
		"after table",
	}}
	segments := DetectSegments(buf, 0, 3)
	if len(segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segments))
	}
	if segments[0].BufStartLine != 0 || segments[0].BufEndLine != 2 {
		t.Fatalf("table segment: expected [0,2], got [%d,%d]",
			segments[0].BufStartLine, segments[0].BufEndLine)
	}
	if segType := getSegmentType(segments[0]); segType != typeTable {
		t.Fatalf("expected table, got %v", segType)
	}
}

func TestDetectBlockquote(t *testing.T) {
	buf := &mockBuffer{lines: []string{
		"> quote line 1",
		"> quote line 2",
		"after quote",
	}}
	segments := DetectSegments(buf, 0, 2)
	if len(segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segments))
	}
	if segments[0].BufStartLine != 0 || segments[0].BufEndLine != 1 {
		t.Fatalf("blockquote segment: expected [0,1], got [%d,%d]",
			segments[0].BufStartLine, segments[0].BufEndLine)
	}
	if segType := getSegmentType(segments[0]); segType != typeBlockquote {
		t.Fatalf("expected blockquote, got %v", segType)
	}
}

func TestDetectBlockquoteWithEmptyLines(t *testing.T) {
	buf := &mockBuffer{lines: []string{
		"> quote",
		"",
		"> more quote",
		"normal",
	}}
	segments := DetectSegments(buf, 0, 3)
	// Blockquote with empty lines: lines 0-2 are blockquote (empty lines included)
	// Line 3 is normal
	if len(segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segments))
	}
	if segType := getSegmentType(segments[0]); segType != typeBlockquote {
		t.Fatalf("segment 0: expected blockquote, got %v", segType)
	}
}

func TestDetectList(t *testing.T) {
	buf := &mockBuffer{lines: []string{
		"- item 1",
		"- item 2",
		"- item 3",
		"after list",
	}}
	segments := DetectSegments(buf, 0, 3)
	if len(segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segments))
	}
	if segments[0].BufStartLine != 0 || segments[0].BufEndLine != 2 {
		t.Fatalf("list segment: expected [0,2], got [%d,%d]",
			segments[0].BufStartLine, segments[0].BufEndLine)
	}
	if segType := getSegmentType(segments[0]); segType != typeList {
		t.Fatalf("expected list, got %v", segType)
	}
}

func TestDetectListWithStar(t *testing.T) {
	buf := &mockBuffer{lines: []string{
		"* item 1",
		"* item 2",
	}}
	segments := DetectSegments(buf, 0, 1)
	if len(segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segments))
	}
	if segType := getSegmentType(segments[0]); segType != typeList {
		t.Fatalf("expected list, got %v", segType)
	}
}

func TestDetectListWithPlus(t *testing.T) {
	buf := &mockBuffer{lines: []string{
		"+ item 1",
		"+ item 2",
	}}
	segments := DetectSegments(buf, 0, 1)
	if len(segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segments))
	}
	if segType := getSegmentType(segments[0]); segType != typeList {
		t.Fatalf("expected list, got %v", segType)
	}
}

func TestDetectNumberedList(t *testing.T) {
	buf := &mockBuffer{lines: []string{
		"1. first",
		"2. second",
		"10. tenth",
	}}
	segments := DetectSegments(buf, 0, 2)
	if len(segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segments))
	}
	if segType := getSegmentType(segments[0]); segType != typeList {
		t.Fatalf("expected list, got %v", segType)
	}
}

func TestDetectHR(t *testing.T) {
	tests := []struct {
		name string
		line string
		want bool
	}{
		{"three dashes", "---", true},
		{"many dashes", "--------------------------", true},
		{"three stars", "***", true},
		{"many stars", "**************************", true},
		{"three underscores", "___", true},
		{"many underscores", "__________________________", true},
		{"two dashes", "--", false},
		{"mixed chars", "-*-", false},
		{"text before", "text---", false},
		{"text after", "---text", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isHR(tt.line); got != tt.want {
				t.Errorf("isHR(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestDetectMixedHR(t *testing.T) {
	buf := &mockBuffer{lines: []string{
		"# Title",
		"---",
		"normal",
	}}
	segments := DetectSegments(buf, 0, 2)
	if len(segments) != 3 {
		t.Fatalf("expected 3 segments, got %d", len(segments))
	}
	if segType := getSegmentType(segments[0]); segType != typeHeading {
		t.Fatalf("segment 0: expected heading, got %v", segType)
	}
	if segType := getSegmentType(segments[1]); segType != typeHR {
		t.Fatalf("segment 1: expected hr, got %v", segType)
	}
	if segType := getSegmentType(segments[2]); segType != typeNormal {
		t.Fatalf("segment 2: expected normal, got %v", segType)
	}
}

func TestDetectMixedContent(t *testing.T) {
	buf := &mockBuffer{lines: []string{
		"# Main Title",
		"",
		"This is a normal with some text.",
		"",
		"## Subtitle",
		"",
		"- list item 1",
		"- list item 2",
		"",
		"> A blockquote",
		"",
		"| Col A | Col B |",
		"|-------|-------|",
		"| data  | data  |",
		"",
		"```",
		"code block",
		"```",
		"",
		"---",
		"",
		"Final normal",
	}}
	segments := DetectSegments(buf, 0, len(buf.lines)-1)

	// Expected segments (15 total):
	// Empty lines within list/blockquote are included in those structures
	// 0: heading, 1: normal (empty), 2: normal, 3: normal (empty)
	// 4: heading, 5: normal (empty)
	// 6: list (6-8, includes empty line 8)
	// 7: blockquote (9-10, includes empty line 10)
	// 8: table (11-13), 9: normal (empty), 10: codeblock
	// 11: normal (empty), 12: hr, 13: normal (empty), 14: normal

	if len(segments) != 15 {
		t.Fatalf("expected 15 segments, got %d", len(segments))
	}

	// Verify first segment is heading
	if segType := getSegmentType(segments[0]); segType != typeHeading {
		t.Fatalf("segment 0: expected heading, got %v", segType)
	}

	// Verify list segment exists
	listFound := false
	for i := range segments {
		if segType := getSegmentType(segments[i]); segType == typeList {
			listFound = true
			break
		}
	}
	if !listFound {
		t.Fatal("no list segment found")
	}

	// Verify table segment exists
	tableFound := false
	for i := range segments {
		if segType := getSegmentType(segments[i]); segType == typeTable {
			tableFound = true
			break
		}
	}
	if !tableFound {
		t.Fatal("no table segment found")
	}

	// Verify codeblock segment exists
	codeFound := false
	for i := range segments {
		if segType := getSegmentType(segments[i]); segType == typeCodeBlock {
			codeFound = true
			break
		}
	}
	if !codeFound {
		t.Fatal("no codeblock segment found")
	}

	// Verify HR segment exists
	hrFound := false
	for i := range segments {
		if segType := getSegmentType(segments[i]); segType == typeHR {
			hrFound = true
			break
		}
	}
	if !hrFound {
		t.Fatal("no hr segment found")
	}
}

func TestDetectNormalOnly(t *testing.T) {
	buf := &mockBuffer{lines: []string{
		"Just some text without any markdown features.",
		"Another normal here.",
		"Yet another line.",
	}}
	segments := DetectSegments(buf, 0, 2)
	if len(segments) != 3 {
		t.Fatalf("expected 3 segments, got %d", len(segments))
	}
	for i := range segments {
		if segType := getSegmentType(segments[i]); segType != typeNormal {
			t.Fatalf("segment %d: expected normal, got %v", i, segType)
		}
	}
}

// TestDetectCodeBlockWithLoneQuote 是 R0 计划 P0 核心回归：
// fence 内落单引号不再吞段（对应对 T1 症状的 fixed3 实验）：
//   - 块在下一条 fence 处正常闭合，不延伸到文件尾
//   - 后续段落（heading / 第二个 fence）各自独立
//   - codeblock 段 IsCodeBlock==true，heading 段为 false（锁 P0→P1 门控契约）
func TestDetectCodeBlockWithLoneQuote(t *testing.T) {
	buf := &mockBuffer{lines: []string{
		"```ts",
		`const txt = String(obj.chatTxt ?? '').replace(/["'}]./, ...)`, // 落单双引号
		"```",
		"### heading",
		"```",
		"another",
		"```",
	}}
	segments := DetectSegments(buf, 0, 6)
	if len(segments) != 3 {
		t.Fatalf("expected 3 segments, got %d", len(segments))
	}
	// 第一段：codeblock [0,2]
	if segType := getSegmentType(segments[0]); segType != typeCodeBlock {
		t.Fatalf("segment 0: expected codeblock, got %v", segType)
	}
	if segments[0].BufStartLine != 0 || segments[0].BufEndLine != 2 {
		t.Fatalf("codeblock segment: expected [0,2], got [%d,%d]",
			segments[0].BufStartLine, segments[0].BufEndLine)
	}
	if !segments[0].IsCodeBlock {
		t.Error("codeblock segment: IsCodeBlock should be true")
	}
	// 第二段：heading [3,3]，不是 codeblock
	if segType := getSegmentType(segments[1]); segType != typeHeading {
		t.Fatalf("segment 1: expected heading, got %v", segType)
	}
	if segments[1].IsCodeBlock {
		t.Error("heading segment: IsCodeBlock should be false")
	}
	// 第三段：codeblock [4,6]
	if segType := getSegmentType(segments[2]); segType != typeCodeBlock {
		t.Fatalf("segment 2: expected codeblock, got %v", segType)
	}
	if segments[2].BufStartLine != 4 || segments[2].BufEndLine != 6 {
		t.Fatalf("second codeblock segment: expected [4,6], got [%d,%d]",
			segments[2].BufStartLine, segments[2].BufEndLine)
	}
	if !segments[2].IsCodeBlock {
		t.Error("second codeblock segment: IsCodeBlock should be true")
	}
}

// TestDetectCodeBlockMidStart 验证 visibleStart 落在未闭合 codeblock 中间时，
// findOpenFenceBefore 回溯初始化能正确把块起点定到开 fence 行。
func TestDetectCodeBlockMidStart(t *testing.T) {
	buf := &mockBuffer{lines: []string{
		"```",
		"code 1",
		"code 2",
		"code 3",
		"```",
		"after",
	}}
	segments := DetectSegments(buf, 2, 3)
	if len(segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segments))
	}
	if segType := getSegmentType(segments[0]); segType != typeCodeBlock {
		t.Fatalf("expected codeblock, got %v", segType)
	}
	if segments[0].BufStartLine != 0 || segments[0].BufEndLine != 3 {
		t.Fatalf("codeblock segment: expected [0,3], got [%d,%d]",
			segments[0].BufStartLine, segments[0].BufEndLine)
	}
}

// TestDetectListBeforeFence 显式锁定 issue #6 行为：未闭合 list 后紧跟 fence 时，
// list 在 fence 前一行关闭，fence 独立成 codeblock，不产生 segment 乱序。
func TestDetectListBeforeFence(t *testing.T) {
	buf := &mockBuffer{lines: []string{
		"- item 1",
		"- item 2",
		"```",
		"code",
		"```",
	}}
	segments := DetectSegments(buf, 0, 4)
	if len(segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segments))
	}
	if segType := getSegmentType(segments[0]); segType != typeList {
		t.Fatalf("segment 0: expected list, got %v", segType)
	}
	if segments[0].BufStartLine != 0 || segments[0].BufEndLine != 1 {
		t.Fatalf("list segment: expected [0,1], got [%d,%d]",
			segments[0].BufStartLine, segments[0].BufEndLine)
	}
	if segType := getSegmentType(segments[1]); segType != typeCodeBlock {
		t.Fatalf("segment 1: expected codeblock, got %v", segType)
	}
	if segments[1].BufStartLine != 2 || segments[1].BufEndLine != 4 {
		t.Fatalf("codeblock segment: expected [2,4], got [%d,%d]",
			segments[1].BufStartLine, segments[1].BufEndLine)
	}
}

// TestDetectFenceIndented 验证带 1~3 空格缩进的 fence 行仍被检出（TrimSpace 从宽处理）。
func TestDetectFenceIndented(t *testing.T) {
	buf := &mockBuffer{lines: []string{
		"  ```",
		"  code",
		"  ```",
		"after",
	}}
	segments := DetectSegments(buf, 0, 3)
	if len(segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segments))
	}
	if segType := getSegmentType(segments[0]); segType != typeCodeBlock {
		t.Fatalf("segment 0: expected codeblock, got %v", segType)
	}
	if segments[0].BufStartLine != 0 || segments[0].BufEndLine != 2 {
		t.Fatalf("codeblock segment: expected [0,2], got [%d,%d]",
			segments[0].BufStartLine, segments[0].BufEndLine)
	}
	if segType := getSegmentType(segments[1]); segType != typeNormal {
		t.Fatalf("segment 1: expected normal, got %v", segType)
	}
}

func TestDetectCodeBlockWithPipe(t *testing.T) {
	// Code blocks containing | should not be detected as tables
	buf := &mockBuffer{lines: []string{
		"```json",
		`{"key": "value", "arr": [1, 2, 3]}`,
		"```",
	}}
	segments := DetectSegments(buf, 0, 2)
	if len(segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segments))
	}
	if segType := getSegmentType(segments[0]); segType != typeCodeBlock {
		t.Fatalf("expected codeblock, not table detection, got %v", segType)
	}
}

func TestDetectEmptyLines(t *testing.T) {
	buf := &mockBuffer{lines: []string{
		"line 1",
		"",
		"line 3",
		"",
		"",
		"line 6",
	}}
	segments := DetectSegments(buf, 0, 5)
	// Each line (including empty lines) is treated as a normal
	// 6 lines = 6 segments
	if len(segments) != 6 {
		t.Fatalf("expected 6 segments, got %d", len(segments))
	}
	for i := range segments {
		if segType := getSegmentType(segments[i]); segType != typeNormal {
			t.Fatalf("segment %d: expected normal, got %v", i, segType)
		}
	}
}

func TestIsListItem(t *testing.T) {
	tests := []struct {
		name string
		line string
		want bool
	}{
		{"dash list", "- item", true},
		{"star list", "* item", true},
		{"plus list", "+ item", true},
		{"numbered list", "1. item", true},
		{"numbered list 10", "10. item", true},
		{"numbered list 100", "100. item", true},
		{"dash no space", "-item", false},
		{"star no space", "*item", false},
		{"single char", "-", false},
		{"text with dash", "some - text", false},
		{"empty string", "", false},
		{"text starting with hash", "# not a list", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isListItem(tt.line); got != tt.want {
				t.Errorf("isListItem(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestDetectVisibleRange(t *testing.T) {
	buf := &mockBuffer{lines: []string{
		"# Title",
		"line 2",
		"line 3",
		"line 4",
		"# Another Title",
	}}
	// Only request visible range 1-3
	segments := DetectSegments(buf, 1, 3)
	if len(segments) != 3 {
		t.Fatalf("expected 3 segments, got %d", len(segments))
	}
	// First segment should be line 1 (normal), not line 0 (heading)
	if segments[0].BufStartLine != 1 {
		t.Fatalf("first segment should start at line 1, got %d", segments[0].BufStartLine)
	}
}

// TestIsTableRow 覆盖 isTableRow 的配对符号扫描逻辑。
// 重点回归：奇数个反引号（或其它未闭合配对符号）不应导致整行漏判为非表格行。
func TestIsTableRow(t *testing.T) {
	tests := []struct {
		name string
		line string
		want bool
	}{
		{"normal table", "| col A | col B |", true},
		{"separator row", "|---|---|---|", true},
		{"cjk table", "| 场景 | 修复前 | 修复后 |", true},
		{"code span with pipe", "| `a|b` | c |", true},
		{"balanced backticks", "| list 跟随 ```go ``` | 覆盖 | 独立 |", true},
		{"odd backticks in cell", "| list 下一行紧跟 ` ```go ` | 覆盖 | 独立 |", true},
		{"single unmatched backtick", "| unmatched ` backtick | data |", true},
		{"unmatched single quote", "| it's a test | data |", true},
		{"only one pipe", "| `unmatched end", false},
		{"no pipe", "plain text", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTableRow(tt.line); got != tt.want {
				t.Errorf("isTableRow(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

// TestDetectTableRowWithOddBackticks 回归 docs/isTableRow-奇数反引号导致表格行漏判.md：
// 表格中某行含奇数个反引号时，不应把表格在该行切断。
func TestDetectTableRowWithOddBackticks(t *testing.T) {
	buf := &mockBuffer{lines: []string{
		"| 场景 | 修复前（bug） | 修复后 |",
		"|---|---|---|",
		"| list 下一行紧跟 ` ```go ` | 覆盖 codeblock | 独立成块 |", // 5 个反引号
		"| blockquote 紧跟 codeblock | 同上 | 独立 |",
	}}
	segments := DetectSegments(buf, 0, 3)
	if len(segments) != 1 {
		t.Fatalf("expected 1 table segment, got %d", len(segments))
	}
	if segType := getSegmentType(segments[0]); segType != typeTable {
		t.Fatalf("expected table, got %v", segType)
	}
	if segments[0].BufStartLine != 0 || segments[0].BufEndLine != 3 {
		t.Fatalf("expected table [0,3], got [%d,%d]",
			segments[0].BufStartLine, segments[0].BufEndLine)
	}
}
