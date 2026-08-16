package md

import (
	"github.com/mattn/go-runewidth"
	"github.com/micro-editor/tcell/v2"
)

// Cell 是渲染管线输出的最小单位：一个屏幕字符。
type Cell struct {
	Rune         rune         // 要显示的字符
	Combining    []rune       // 组合字符（通常为 nil）
	Style        tcell.Style  // 颜色和字体样式
	BufLine      int          // 对应的 buffer 行号，装饰行为 -1
	BufX         int          // 对应 buffer 行内的 rune 偏移，装饰行为 -1
	IsDecorative bool         // true = 装饰字符，点击忽略
}

// RenderedRow 是渲染后的一行屏幕输出。
type RenderedRow struct {
	Cells   []Cell
	BufLine int  // 这行对应的 buffer 行号（wrap 续行也保留真实行号，display 层决定是否显示行号）
}

// RenderedSegment 是一个渲染片的完整渲染输出。
type RenderedSegment struct {
	Rows         []RenderedRow
	BufStartLine int           // 片起始 buffer 行
	BufEndLine   int           // 片结束 buffer 行（含）
}

// SegmentMeta 是检测步骤输出的轻量元数据，不包含渲染内容。
// 缓存到 BufWindow 上供 Scroll/Diff 使用。
type SegmentMeta struct {
	BufStartLine int
	BufEndLine   int
	RowBufLines  []int  // RowBufLines[i] = 第 i 个 screen row 的 BufLine（装饰行 = -1）
}

// Segment 是检测步骤的输出单位。每一行 buffer 都属于某个 Segment。
type Segment struct {
	BufStartLine int
	BufEndLine   int
	// VisibleStart/VisibleEnd 由 display 层的 filterSegmentsToVisible() 设置，
	// 标记该 segment 在当前 viewport 中实际需要渲染的 buffer 行范围。
	// 若整个 segment 都可见，则 VisibleStart == BufStartLine && VisibleEnd == BufEndLine。
	// 现阶段 renderer 只读 BufStartLine/BufEndLine，忽略 VisibleStart/VisibleEnd。
	VisibleStart int
	VisibleEnd   int
	// IsCodeBlock 标记该段是否为 codeblock。render 层据此决定是否做污染检测。
	IsCodeBlock bool
	// Render 是渲染函数。接收完整 Segment，从 cfg.Buf 取 lines，返回渲染结果。
	// Step 0 阶段只输出背景色。
	Render func(seg Segment, width int, cfg MDConfig) *RenderedSegment
}

// linesFromBuf 从 BufferReader 读取 start..end 行（含两端），返回 []string。
// 所有 renderer 通过此函数从 cfg.Buf 取行内容。
//
// 边界行为：
//   - buf == nil → 返回 nil（renderer 退化到纯背景色渲染）
//   - start > end → 返回空切片 []string{}（非 nil）
//   - start/end 越界 → 自动 clamp 到有效范围
func linesFromBuf(buf BufferReader, start, end int) []string {
	if buf == nil {
		return nil
	}
	lines := make([]string, 0, end-start+1)
	for i := start; i <= end && i < buf.LinesNum(); i++ {
		lines = append(lines, string(buf.LineBytes(i)))
	}
	return lines
}

// renderLinesWithBg 是 Step 0 的公共渲染逻辑：
// 将 lines 逐字符输出为 Cell，每行填充到 width 列，全部使用 bgStyle。
// 返回的 RenderedSegment 中所有 BufLine 都是相对行号（从 0 开始）。
// 注意：需要正确处理 CJK 等宽字符（占 2 列），宽字符后补一个空占位 Cell。
func renderLinesWithBg(lines []string, width int, bgStyle tcell.Style) *RenderedSegment {
	result := &RenderedSegment{}
	for lineIdx, line := range lines {
		row := RenderedRow{
			BufLine: lineIdx, // 相对行号，displayBufferMD 调整
		}
		col := 0
		runeIdx := 0
		for _, r := range line {
			rw := runewidth.RuneWidth(r)
			row.Cells = append(row.Cells, Cell{
				Rune:    r,
				Style:   bgStyle,
				BufLine: lineIdx,
				BufX:    runeIdx,
			})
			col += rw
			runeIdx++
			// 宽字符占 2 列，补一个空占位 Cell 保持背景色连续
			if rw == 2 {
				row.Cells = append(row.Cells, Cell{
					Rune:    ' ',
					Style:   bgStyle,
					BufLine: lineIdx,
					BufX:    -1,
				})
			}
		}
		// 填充到 width
		for ; col < width; col++ {
			row.Cells = append(row.Cells, Cell{
				Rune:    ' ',
				Style:   bgStyle,
				BufLine: lineIdx,
				BufX:    -1,
			})
		}
		result.Rows = append(result.Rows, row)
	}
	return result
}


