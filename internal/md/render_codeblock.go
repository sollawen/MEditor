package md

import (
	"strings"

	"github.com/mattn/go-runewidth"
	"github.com/micro-editor/tcell/v2"
)

// extractLang 从围栏行提取语言名。
// lines[0] 形如 "```python"、"`````python"、"```" 等
func extractLang(fenceLine string) string {
	trimmed := strings.TrimSpace(fenceLine)
	lang := "text"
	// 去掉开头的反引号序列（至少 3 个）
	if idx := strings.Index(trimmed, "```"); idx >= 0 && idx < 4 {
		rest := strings.TrimSpace(trimmed[idx+3:])
		if rest != "" {
			lang = rest
		}
	}
	return lang
}

// isClosingFence 判断行是否为闭合围栏行。与 detect 层 isFenceLine 同源：反引号或波浪线。
func isClosingFence(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}

// makeTopBorder 创建顶边框行（┌ + ─ + lang + ─ 填满 width）。
// 所有 Cell 标记为装饰。
func makeTopBorder(lang string, width int, borderStyle tcell.Style, labelStyle tcell.Style) RenderedRow {
	row := RenderedRow{
		BufLine: -1, // 装饰行
		Cells:   make([]Cell, 0, width),
	}

	// 左上角 ┌
	row.Cells = append(row.Cells, Cell{
		Rune:         '┌', // U+250C
		Style:        borderStyle,
		BufLine:      -1,
		BufX:         -1,
		IsDecorative: true,
	})

	// lang 前面的 ─（4 个）
	for i := 0; i < 4; i++ {
		row.Cells = append(row.Cells, Cell{
			Rune:         '─', // U+2500
			Style:        borderStyle,
			BufLine:      -1,
			BufX:         -1,
			IsDecorative: true,
		})
	}

	// lang 前面的空格
	row.Cells = append(row.Cells, Cell{
		Rune:         ' ',
		Style:        borderStyle,
		BufLine:      -1,
		BufX:         -1,
		IsDecorative: true,
	})

	// 语言名（使用 labelStyle）
	for _, r := range lang {
		row.Cells = append(row.Cells, Cell{
			Rune:         r,
			Style:        labelStyle,
			BufLine:      -1,
			BufX:         -1,
			IsDecorative: true,
		})
	}

	// lang 后面的空格
	row.Cells = append(row.Cells, Cell{
		Rune:         ' ',
		Style:        borderStyle,
		BufLine:      -1,
		BufX:         -1,
		IsDecorative: true,
	})

	// 填充到 width
	used := 1 + 4 + 1 + len(lang) + 1
	for i := used; i < width; i++ {
		row.Cells = append(row.Cells, Cell{
			Rune:         '─', // U+2500
			Style:        borderStyle,
			BufLine:      -1,
			BufX:         -1,
			IsDecorative: true,
		})
	}

	return row
}

// makeBottomBorder 创建底边框行（└ + ─ 填满 width）。
// 所有 Cell 标记为装饰。
func makeBottomBorder(width int, borderStyle tcell.Style) RenderedRow {
	row := RenderedRow{
		BufLine: -1, // 装饰行
		Cells:   make([]Cell, 0, width),
	}

	// 左下角 └
	row.Cells = append(row.Cells, Cell{
		Rune:         '└', // U+2514
		Style:        borderStyle,
		BufLine:      -1,
		BufX:         -1,
		IsDecorative: true,
	})

	// 横线填满 width
	for i := 1; i < width; i++ {
		row.Cells = append(row.Cells, Cell{
			Rune:         '─', // U+2500
			Style:        borderStyle,
			BufLine:      -1,
			BufX:         -1,
			IsDecorative: true,
		})
	}

	return row
}

// RenderCodeBlock 渲染代码块，实现带 ASCII 细边框的渲染。
// 布局：
//   ┌────python──────────────────────────────  ← 顶边框
//   │ def function(abd:int):                 ← 代码行
//   │     self ddd = abd                     ← 代码行（可能 wrap）
//   └─────────────────────────────────────────  ← 底边框（未闭合时不画）
func RenderCodeBlock(seg Segment, width int, cfg MDConfig) *RenderedSegment {
	lines := linesFromBuf(cfg.Buf, seg.BufStartLine, seg.BufEndLine)
	if !cfg.MDCodeBlock || len(lines) == 0 {
		return renderLinesWithBg(lines, width, cfg.Colorscheme.DefStyle)
	}

	result := &RenderedSegment{}

	// ── 1. 从 lines[0] 提取语言名 ──
	lang := extractLang(lines[0])

	// ── 2. 查 md-codeblock → colorscheme 得到背景色 ──
	codeStyle := resolveStyle(cfg.Colorscheme.Styles, "md-codeblock", cfg.Colorscheme.DefStyle)
	_, codeBg, _ := codeStyle.Decompose()

	// ── 3. 边框样式：从 colorscheme 查 md-frame，背景色跟随代码块 ──
	borderStyle := resolveStyle(cfg.Colorscheme.Styles, "md-frame", cfg.Colorscheme.DefStyle).Background(codeBg)
	labelStyle := resolveStyle(cfg.Colorscheme.Styles, "md-frame-label", cfg.Colorscheme.DefStyle).Background(codeBg)

	// ── 4. 判断是否闭合 ──
	isClosed := len(lines) > 1 && isClosingFence(lines[len(lines)-1])

	// ── 5. 顶边框（开围栏是真实 buffer 行，用相对行号 0 = lines[0]） ──
	topRow := makeTopBorder(lang, width, borderStyle, labelStyle)
	topRow.BufLine = 0
	result.Rows = append(result.Rows, topRow)

	// ── 6. 代码内容行 ──
	// 代码行范围：lines[1:n-1]（闭合时）或 lines[1:n]（未闭合时）
	codeStart := 1
	codeEnd := len(lines)
	if isClosed {
		codeEnd = len(lines) - 1
	}

	// 前缀：│ + 空格
	prefixCells := []Cell{
		{Rune: '│', Style: borderStyle, BufLine: -1, BufX: -1, IsDecorative: true},
		{Rune: ' ', Style: borderStyle, BufLine: -1, BufX: -1, IsDecorative: true},
	}
	contentWidth := width - 2
	tabSize := cfg.TabSize
	if tabSize <= 0 {
		tabSize = 4
	}

	for lineIdx := codeStart; lineIdx < codeEnd; lineIdx++ {
		line := lines[lineIdx]
		// 代码逐 rune 转 Cell，展开 tab
		contentCells := make([]Cell, 0, len(line)+tabSize)
		col := 0
		runeIdx := 0
		for _, r := range line {
			if r == '\t' {
				// tab 展开为空格，对齐到 tabSize 网格
				ts := tabSize - (col % tabSize)
				for j := 0; j < ts; j++ {
					contentCells = append(contentCells, Cell{
						Rune:    ' ',
						Style:   codeStyle,
						BufLine: lineIdx,
						BufX:    runeIdx,
					})
				}
				col += ts
			} else {
				rw := runewidth.RuneWidth(r)
				contentCells = append(contentCells, Cell{
					Rune:    r,
					Style:   codeStyle,
					BufLine: lineIdx,
					BufX:    runeIdx,
				})
				col += rw
			}
			runeIdx++
		}

		// 字符级硬切 wrap（到边界就换行，不考虑词边界）
		contentRows := wrapCellsHard(contentCells, contentWidth, lineIdx, codeStyle)

		// 首行和续行都拼上 │ 前缀
		for _, row := range contentRows {
			row.Cells = append(prefixCells, row.Cells...)
			result.Rows = append(result.Rows, row)
		}
	}

	// ── 7. 底边框（仅闭合时，闭围栏是真实 buffer 行，用相对行号 len(lines)-1） ──
	if isClosed {
		bottomRow := makeBottomBorder(width, borderStyle)
		bottomRow.BufLine = len(lines) - 1
		result.Rows = append(result.Rows, bottomRow)
	}

	return result
}

// wrapCellsHard 字符级硬切 wrap：逐 Cell 填满 maxWidth 就换行。
// 代码块专用，不像 wrapCells 那样按词分组。
// BufLine 规则：Row.BufLine 始终保留 bufLine；Cell 的 BufLine 保留各自原始值。
func wrapCellsHard(cells []Cell, maxWidth int, bufLine int, padStyle tcell.Style) []RenderedRow {
	if len(cells) == 0 {
		return []RenderedRow{makePaddingRow(maxWidth, bufLine, padStyle)}
	}

	var rows []RenderedRow
	var curCells []Cell
	curWidth := 0

	for _, c := range cells {
		rw := runewidth.RuneWidth(c.Rune)

		if curWidth+rw > maxWidth {
			// 当前行放不下了，输出当前行
			rows = appendRow(rows, curCells, maxWidth, bufLine, padStyle, true)
			curCells = nil
			curWidth = 0
		}

		curCells = append(curCells, c)
		curWidth += rw
	}

	// 最后一行
	if len(curCells) > 0 {
		rows = appendRow(rows, curCells, maxWidth, bufLine, padStyle, true)
	} else if len(rows) == 0 {
		rows = appendRow(rows, nil, maxWidth, bufLine, padStyle, true)
	}

	return rows
}