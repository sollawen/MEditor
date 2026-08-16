package md

import "strings"

// BufferReader 是 detect.go 对 buffer 的最小依赖接口。
// 由 BufWindow 调用 DetectSegments 时传入 w.Buf（*buffer.Buffer 满足此接口）。
type BufferReader interface {
	LinesNum() int
	LineBytes(n int) []byte
}

// detectState 表示扫描器当前的状态。
type detectState int

const (
	stateNormal detectState = iota
	stateBlockquote
	stateTable
	stateList
)

// DetectSegments 扫描 buffer 的可见区域，返回 []Segment。
// 每个 Segment 标记了它负责的 buffer 行范围和渲染函数。
// visibleStart/visibleEnd 是 buffer 行号范围（含两端）。
// buf 是 buffer 引用，用于读取行内容。
func DetectSegments(
	buf BufferReader,
	visibleStart, visibleEnd int,
) []Segment {
	segments := []Segment{}
	state := stateNormal
	var startLine int // 当前多行结构起始行（blockquote/table/list 用）

	// codeblock 边界跟踪：fence 行内容驱动，免疫嵌入语言 state 粘滞。
	// 回溯初始化：块外为 -1，块内为开 fence 行号。
	codeblockStart := findOpenFenceBefore(buf, visibleStart)

	for y := visibleStart; y <= visibleEnd; y++ {
		if y >= buf.LinesNum() {
			break
		}

		trimmed := strings.TrimSpace(string(buf.LineBytes(y)))

		// ── Codeblock 边界：fence 行字符串匹配 ──
		if isFenceLine(trimmed) {
			if codeblockStart == -1 {
				// 开 fence：先关闭未闭合的多行结构（list/blockquote/table）。
				// 否则它们的兜底分支会把 BufEndLine 扩到 visibleEnd，
				// 吞掉 codeblock 并导致 segment 顺序倒挂（issue #6）。
				closeOpenStructures(&segments, &state, startLine, y)
				codeblockStart = y
			} else {
				// 闭 fence：emit codeblock 段（含两侧行）
				segments = append(segments, Segment{
					BufStartLine: codeblockStart,
					BufEndLine:   y,
					Render:       RenderCodeBlock,
					IsCodeBlock:  true,
				})
				codeblockStart = -1
			}
			continue // fence 行已归入 codeblock，不做字符串匹配
		}
		if codeblockStart != -1 {
			continue // 块内行：归入 codeblock，不产生独立 segment
		}

		// ── 非 codeblock 行：字符串匹配 ──
		reprocess := false

		switch state {
		case stateNormal:
			if strings.HasPrefix(trimmed, ">") {
				state = stateBlockquote
				startLine = y
			} else if isTableRow(trimmed) {
				state = stateTable
				startLine = y
			} else if isListItem(trimmed) {
				state = stateList
				startLine = y
			} else if strings.HasPrefix(trimmed, "#") {
				segments = append(segments, Segment{
					BufStartLine: y,
					BufEndLine:   y,
					Render:       RenderHeading,
				})
			} else if isHR(trimmed) {
				segments = append(segments, Segment{
					BufStartLine: y,
					BufEndLine:   y,
					Render:       RenderHR,
				})
			} else {
				segments = append(segments, Segment{
					BufStartLine: y,
					BufEndLine:   y,
					Render:       RenderNormal,
				})
			}

		case stateBlockquote:
			if strings.HasPrefix(trimmed, ">") {
				// 继续收集引用块行
			} else {
				segments = append(segments, Segment{
					BufStartLine: startLine,
					BufEndLine:   y - 1,
					Render:       RenderBlockquote,
				})
				state = stateNormal
				reprocess = true
			}

		case stateTable:
			if isTableRow(trimmed) {
				// 继续收集表格行
			} else {
				segments = append(segments, Segment{
					BufStartLine: startLine,
					BufEndLine:   y - 1,
					Render:       RenderTable,
				})
				state = stateNormal
				reprocess = true
			}

		case stateList:
			if isListItem(trimmed) {
				// 继续收集列表项
			} else {
				segments = append(segments, Segment{
					BufStartLine: startLine,
					BufEndLine:   y - 1,
					Render:       RenderList,
				})
				state = stateNormal
				reprocess = true
			}
		}

		if reprocess {
			y-- // 回退：下一轮以 stateNormal 重新处理当前行
			continue
		}
	}

	// 未闭合的 codeblock
	if codeblockStart != -1 {
		segments = append(segments, Segment{
			BufStartLine: codeblockStart,
			BufEndLine:   visibleEnd,
			Render:       RenderCodeBlock,
			IsCodeBlock:  true,
		})
	}

	// 未闭合的 blockquote/table/list
	switch state {
	case stateBlockquote:
		segments = append(segments, Segment{
			BufStartLine: startLine,
			BufEndLine:   visibleEnd,
			Render:       RenderBlockquote,
		})
	case stateTable:
		segments = append(segments, Segment{
			BufStartLine: startLine,
			BufEndLine:   visibleEnd,
			Render:       RenderTable,
		})
	case stateList:
		segments = append(segments, Segment{
			BufStartLine: startLine,
			BufEndLine:   visibleEnd,
			Render:       RenderList,
		})
	}

	return segments
}

// isTableRow 判断是否为表格行。
// 判定规则：第一个字符必须是 |，且在配对符号之外至少还有一个 |
func isTableRow(s string) bool {
	if len(s) == 0 || s[0] != '|' {
		return false
	}

	// 从第一个 | 之后扫描，跳过配对符号包裹的内容，
	// 在跳过范围之外找至少一个 |
	i := 1
	for i < len(s) {
		if s[i] == '|' {
			return true
		}
		// 跳过配对符号
		var close byte
		switch s[i] {
		case '`':
			close = '`'
		case '"':
			close = '"'
		case '\'':
			close = '\''
		case '(':
			close = ')'
		case '[':
			close = ']'
		case '{':
			close = '}'
		default:
			i++
			continue
		}
		// 跳过开符号本身，然后找闭符号
		start := i
		i++
		found := false
		for i < len(s) {
			if s[i] == close {
				found = true
				i++ // 跳过闭符号
				break
			}
			i++
		}
		// 未找到闭符号：把开符号当普通字符，从下一个位置继续扫描。
		// 否则奇数个反引号（或其它未闭合配对符号）会让 i 冲到字符串末尾，
		// 吞掉剩余行里真正的 | 列分隔符，导致整行漏判为非表格行。
		if !found {
			i = start + 1
		}
	}
	return false
}

// isFenceLine 判断是否 fence 行：≥3 个反引号或波浪线开头。
// 入参是已 TrimSpace 的行；只看前缀不区分开/闭，由主循环 codeblockStart 状态区分。
func isFenceLine(trimmed string) bool {
	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}

// findOpenFenceBefore 返回 visibleStart 所在未闭合 codeblock 的开 fence 行号，块外返回 -1。
// 从第 0 行扫到 visibleStart-1，fence 行奇数次出现后在块内（记住行号），偶数次后回块外。
// 生产热路径恒 visibleStart==0（buffer.go 全量调用），本函数零开销。
func findOpenFenceBefore(buf BufferReader, visibleStart int) int {
	open := -1
	for y := 0; y < visibleStart && y < buf.LinesNum(); y++ {
		if isFenceLine(strings.TrimSpace(string(buf.LineBytes(y)))) {
			if open == -1 {
				open = y
			} else {
				open = -1
			}
		}
	}
	return open
}

// closeOpenStructures 在进入 codeblock 前关闭未闭合的多行结构（list/blockquote/table）。
// emit [startLine, y-1] 段并把 state 归 normal；startLine 由主循环后续匹配重新赋值。
func closeOpenStructures(segments *[]Segment, st *detectState, startLine, y int) {
	switch *st {
	case stateBlockquote:
		*segments = append(*segments, Segment{BufStartLine: startLine, BufEndLine: y - 1, Render: RenderBlockquote})
		*st = stateNormal
	case stateTable:
		*segments = append(*segments, Segment{BufStartLine: startLine, BufEndLine: y - 1, Render: RenderTable})
		*st = stateNormal
	case stateList:
		*segments = append(*segments, Segment{BufStartLine: startLine, BufEndLine: y - 1, Render: RenderList})
		*st = stateNormal
	}
}

// isHR 判断是否为水平分割线：- = * _ 任一字符重复 ≥3 次。
func isHR(s string) bool {
	if len(s) < 3 {
		return false
	}
	c := s[0]
	if c != '-' && c != '=' && c != '*' && c != '_' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] != c {
			return false
		}
	}
	return true
}

// isListItem 判断是否为列表项：以 "- " / "* " / "+ " 开头，或 "1. " 等数字序号
func isListItem(s string) bool {
	if len(s) < 2 {
		return false
	}
	if s[0] == '-' || s[0] == '*' || s[0] == '+' {
		return s[1] == ' '
	}
	// 数字序号: "1. " / "12. " 等
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i > 0 && i+1 < len(s) && s[i] == '.' && s[i+1] == ' ' {
		return true
	}
	return false
}