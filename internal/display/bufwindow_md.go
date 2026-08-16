package display

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
	"github.com/micro-editor/micro/v2/internal/buffer"
	"github.com/micro-editor/micro/v2/internal/config"
	"github.com/micro-editor/micro/v2/internal/md"
	"github.com/micro-editor/micro/v2/internal/screen"
	"github.com/micro-editor/micro/v2/internal/util"
	"github.com/micro-editor/micro/v2/pkg/highlight"
	"github.com/micro-editor/tcell/v2"
)

// ===== 诊断日志 =====
// 对齐 micro 原生 util.Debug 机制：仅当 util.Debug == "ON"（即 `make build-dbg`）时
// 追加写 /tmp/microNeo_debug.log；release 构建默认 OFF，零开销。
func dbgLog(format string, args ...any) {
	if util.Debug != "ON" {
		return
	}
	f, err := os.OpenFile("/tmp/microNeo_debug.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	ts := time.Now().Format("15:04:05.000")
	f.WriteString(ts + " " + fmt.Sprintf(format, args...) + "\n")
}

// DbgLog 把 dbgLog 暴露给其他包（如 action）复用同一份诊断日志（/tmp/microNeo_debug.log），
// 不另起 sink。同样只在 util.Debug=="ON"（make build-dbg）时写入。
func DbgLog(format string, args ...any) { dbgLog(format, args...) }

// segRangeStr 返回 segment 的 [Start..End] 字符串，nil 返回 "nil"
func segRangeStr(s *md.Segment) string {
	if s == nil {
		return "nil"
	}
	return fmt.Sprintf("[seg L%d..L%d]", s.BufStartLine, s.BufEndLine)
}


// SetMDConfig 设置 MD 渲染配置。
func (w *BufWindow) SetMDConfig(cfg md.MDConfig) {
	w.mdConfig = cfg
}

// ensureMDConfigReady 检查 MDConfig 的 Colorscheme 是否与当前全局 Colorscheme 一致。
// 启动时 initMDConfig 在 InitColorscheme 之前被调用，拿到的是空值。
// 运行时切换 colorscheme 也会导致指针变化。
// 通过指针比较（比较 map 变量的地址），在渲染时按需刷新，无需改动任何 micro 原生代码。
func (w *BufWindow) ensureMDConfigReady() {
	if w.mdConfig.Colorscheme.StylesRef == nil || w.mdConfig.Colorscheme.StylesRef != &config.Colorscheme {
		w.mdConfig.Colorscheme.Styles = config.Colorscheme
		w.mdConfig.Colorscheme.StylesRef = &config.Colorscheme
		w.mdConfig.Colorscheme.DefStyle = config.DefStyle
	}
}

// hasCursorInside 判定 seg 内是否有任意光标。
//
// 回退判定（displayBufferMD 主循环使用）：
//   - editMode=true（编辑模式）+ 光标在 seg 内 → 走原生
//   - editMode=false（阅读模式）             → 全部走 MD，不回退
func hasCursorInside(seg md.Segment, cursors []*buffer.Cursor) bool {
	for _, c := range cursors {
		// 检查1：光标本身所在行是否在 segment 内
		if c.Y >= seg.BufStartLine && c.Y <= seg.BufEndLine {
			return true
		}
		// 检查2：选区是否与 segment 有交集（仅当存在有效选区时）
		if c.HasSelection() {
			selStart := c.CurSelection[0].Y
			selEnd := c.CurSelection[1].Y
			if selStart > selEnd {
				selStart, selEnd = selEnd, selStart
			}
			if selStart <= seg.BufEndLine && seg.BufStartLine <= selEnd {
				return true
			}
		}
	}
	return false
}

// renderSegmentMD 渲染单个 MD segment 到 screen。
// 返回签名只返回 newVY，元数据同源写入 sb.setRowMeta（方案B 单一数据源）。
func (w *BufWindow) renderSegmentMD(
	seg md.Segment, vY int,
) (newVY int) {
	bufWidth := w.bufWidth
	renderLimit := w.renderLimit() // displayToBuffer 模式下为 2×

	// ★ 注入 Buf 到 MDConfig，renderer 通过 cfg.Buf 自行取 lines
	w.mdConfig.Buf = w.Buf

	// ★ 直接传 seg 给 renderer，不再由 display 层取 lines
	rendered := seg.Render(seg, bufWidth, w.mdConfig)

	// 绝对化 BufLine（renderer 产出相对行号，display 层统一绝对化）
	// §4 坐标系说明：MD 渲染器内部产出的是 segment 内相对行号，
	// renderSegmentMD:76-84 统一绝对化：row.BufLine >= 0 的 += seg.BufStartLine
	// 装饰行（-1）不加，保持 -1
	for ri := range rendered.Rows {
		if rendered.Rows[ri].BufLine >= 0 {
			rendered.Rows[ri].BufLine += seg.BufStartLine
		}
		for ci := range rendered.Rows[ri].Cells {
			if rendered.Rows[ri].Cells[ci].BufLine >= 0 {
				rendered.Rows[ri].Cells[ci].BufLine += seg.BufStartLine
			}
		}
	}

	// ★ lineStyles 预计算：覆盖完整范围 BufStartLine~BufEndLine
	lineStyles := map[int][]tcell.Style{}
	for bufLine := seg.BufStartLine; bufLine <= seg.BufEndLine; bufLine++ {
		line := w.Buf.Line(bufLine)
		lineStyles[bufLine] = w.expandLineStyles(bufLine, utf8.RuneCountInString(line), config.DefStyle, seg.IsCodeBlock)
	}

	// ★ 预扫描：找到第一个内容行的 BufLine（绝对行号）
	// 前置装饰行归属此行。若第一个内容行已滚出 viewport，前置装饰行也滚出。
	firstContentBufLine := -1
	for _, row := range rendered.Rows {
		if row.BufLine >= 0 {
			firstContentBufLine = row.BufLine
			break
		}
	}

	// ★ 渲染 cells（写入 sb） + 元数据（写入 sb.rows[vY].line/segRow）：
	//     根据 VisibleStart/VisibleEnd 过滤
	lastBufLine := -1
	lastContentBufLine := -1 // 追踪最近的内容行，用于装饰行可见性判断
	segRow := 0              // segmentRow：同一 buffer 行的第几个 softwrap 段
	for _, row := range rendered.Rows {
		if vY >= renderLimit {
			break
		}

		// 可见性判断（用于决定是否渲染，不影响 viewportRowmap 写入）
		effectiveLine := row.BufLine
		if effectiveLine < 0 {
			if lastContentBufLine >= 0 {
				effectiveLine = lastContentBufLine // 装饰行复用最近内容行
			} else if firstContentBufLine >= 0 {
				effectiveLine = firstContentBufLine // 前置装饰行归属第一个内容行
			}
		}
		if effectiveLine < seg.VisibleStart || effectiveLine > seg.VisibleEnd {
			// 不可见行：更新追踪变量但不渲染
			if row.BufLine >= 0 {
				lastContentBufLine = row.BufLine
			}
			lastBufLine = row.BufLine
			continue
		}

		// ★ 只写 sb.setRowMeta（方案B同源生成；viewportRowmap 已迁移到 sb 查询）
		// effectiveLine 逻辑仅用于可见性判断
		// sb.setRowMeta 内部有 bounds 检查，无需外层 vY 条件
		if row.BufLine < 0 {
			w.sb.setRowMeta(vY, -1, -1) // 装饰行
		} else if row.BufLine == lastBufLine {
			segRow++ // 同一 buffer 行的续段
			w.sb.setRowMeta(vY, row.BufLine, segRow)
		} else {
			segRow = 0 // 新 buffer 行
			w.sb.setRowMeta(vY, row.BufLine, 0)
		}

		// 更新追踪
		if row.BufLine >= 0 {
			lastContentBufLine = row.BufLine
		}

		// 以下渲染逻辑不变
		softwrapped := row.BufLine >= 0 && row.BufLine == lastBufLine
		w.drawGutterAndLineNumMD(vY, row.BufLine, softwrapped)
		lastBufLine = row.BufLine

		for col, cell := range row.Cells {
			screenX := w.X + w.gutterOffset + col
			screenY := w.Y + vY
			if screenX < w.X+w.gutterOffset || screenX >= w.X+w.gutterOffset+bufWidth {
				continue
			}
			style := cell.Style
			// 确保背景色：如果 cell 没有设置背景色，使用 DefStyle 的背景色
			_, bg, _ := style.Decompose()
			if bg == tcell.ColorDefault {
				_, defBg, _ := config.DefStyle.Decompose()
				style = style.Background(defBg)
			}
			// 记住 cell 原有的非默认背景色（如代码块背景）
			_, cellBg, _ := cell.Style.Decompose()
			if cell.BufLine >= 0 && cell.BufX >= 0 {
				if styles, ok := lineStyles[cell.BufLine]; ok && cell.BufX < len(styles) {
					style = styles[cell.BufX] // highlight 的完整颜色（前景+背景）
					// 如果 cell 有明确的非默认背景色（如代码块），保留它
					if cellBg != tcell.ColorDefault {
						style = style.Background(cellBg)
					}
				}
				// else: 保持 cell.Style（fallback，不覆盖）
			}
			// 叠加 renderInline 的文本属性
			_, _, attrs := cell.Style.Decompose()
			if attrs&tcell.AttrBold != 0 {
				style = style.Bold(true)
			}
			if attrs&tcell.AttrItalic != 0 {
				style = style.Italic(true)
			}
			if attrs&tcell.AttrUnderline != 0 {
				style = style.Underline(true)
			}
			if attrs&tcell.AttrStrikeThrough != 0 {
				style = style.StrikeThrough(true)
			}
			w.setCell(screenX, screenY, cell.Rune, cell.Combining, style)
		}

		vY++
	}

	return vY
}



// renderSegmentNative 用原生逻辑渲染一个 segment 到 BufWindow。
// seg 必须已经过 filterSegmentsToVisible 裁剪（BufStartLine/BufEndLine 在可见区内）。
// 不修改 displayBuffer()，从那里复制主循环并按需调整。
// 返回签名只返回 newVY，元数据同源写入 sb.setRowMeta（方案B 单一数据源）。
func (w *BufWindow) renderSegmentNative(
	seg md.Segment, startVY int,
) (newVY int) {
	b := w.Buf
	renderLimit := w.renderLimit() // displayToBuffer 模式下为 2×

	maxWidth := w.gutterOffset + w.bufWidth

	if b.ModifiedThisFrame {
		if b.Settings["diffgutter"].(bool) {
			b.UpdateDiff()
		}
		b.ModifiedThisFrame = false
	}

	var matchingBraces []buffer.Loc
	// bracePairs is defined in buffer.go
	if b.Settings["matchbrace"].(bool) {
		for _, c := range b.GetCursors() {
			if c.HasSelection() {
				continue
			}

			mb, left, found := b.FindMatchingBrace(c.Loc)
			if found {
				matchingBraces = append(matchingBraces, mb)
				if !left {
					if b.Settings["matchbracestyle"].(string) != "highlight" {
						matchingBraces = append(matchingBraces, c.Loc)
					}
				} else {
					matchingBraces = append(matchingBraces, c.Loc.Move(-1, b))
				}
			}
		}
	}

	lineNumStyle := config.DefStyle
	if style, ok := config.Colorscheme["line-number"]; ok {
		lineNumStyle = style
	}
	curNumStyle := config.DefStyle
	if style, ok := config.Colorscheme["current-line-number"]; ok {
		if !b.Settings["cursorline"].(bool) {
			curNumStyle = lineNumStyle
		} else {
			curNumStyle = style
		}
	}

	softwrap := b.Settings["softwrap"].(bool)
	wordwrap := softwrap && b.Settings["wordwrap"].(bool)

	tabsize := util.IntOpt(b.Settings["tabsize"])
	colorcolumn := util.IntOpt(b.Settings["colorcolumn"])

	// this represents the current draw position
	// within the current window
	vloc := buffer.Loc{X: 0, Y: startVY}

	// this represents the current draw position in the buffer (char positions)
	// ★ VisibleStart 是 segment 在 viewport 中的实际起始行（可能 > BufStartLine）
	bloc := buffer.Loc{X: -1, Y: seg.VisibleStart}

	cursors := b.GetCursors()

	curStyle := config.DefStyle

	// Parse showchars which is in the format of key1=val1,key2=val2,...
	spacechars := " "
	tabchars := b.Settings["indentchar"].(string)
	var indentspacechars string
	var indenttabchars string
	for _, entry := range strings.Split(b.Settings["showchars"].(string), ",") {
		split := strings.SplitN(entry, "=", 2)
		if len(split) < 2 {
			continue
		}
		key, val := split[0], split[1]
		switch key {
		case "space":
			spacechars = val
		case "tab":
			tabchars = val
		case "ispace":
			indentspacechars = val
		case "itab":
			indenttabchars = val
		}
	}

	// ★ softwrap offset 判断
	// 语义：当 viewport 起始位置正好是 segment 可见范围的起始行时，
	//       需要补偿 softwrap row 偏移（该 buffer 行上方已有部分 row 滚出屏幕）。
	// 等价性证明：
	//   filterSegmentsToVisible 设置 VisibleStart = max(BufStartLine, startY)
	//   displayBufferMD 计算 visibleStart = w.StartLine.Line
	//   因此 VisibleStart == w.StartLine.Line 当且仅当
	//   segment 的可见范围恰好从 viewport 起始行开始。
	//   这与原逻辑（截断后 BufStartLine == w.StartLine.Line）完全等价。
	if softwrap && seg.VisibleStart == w.StartLine.Line {
		// 视口可能从该 buffer 行的中间 screen row 开始，
		// 需要和原版一样跳过上方已滚出屏幕的部分
		vloc.Y -= w.StartLine.Row
	}

	// ★ 循环条件用 seg.VisibleEnd 替代 seg.BufEndLine
	for ; vloc.Y < renderLimit && bloc.Y <= seg.VisibleEnd; vloc.Y++ {
		// [MOD 5] 记录当前行起始 vY（行内 wrap 时用于差值）
		lineStartVY := vloc.Y

		vloc.X = 0

		currentLine := false
		for _, c := range cursors {
			if !c.HasSelection() && bloc.Y == c.Y && w.active {
				currentLine = true
				break
			}
		}

		s := lineNumStyle
		if currentLine {
			s = curNumStyle
		}

		if vloc.Y >= 0 {
			if w.hasMessage {
				w.drawGutter(&vloc, &bloc)
			}

			if b.Settings["diffgutter"].(bool) {
				w.drawDiffGutter(s, false, &vloc, &bloc)
			}

			if b.Settings["ruler"].(bool) {
				w.drawLineNum(s, false, &vloc, &bloc)
			}
		} else {
			vloc.X = w.gutterOffset
		}

		bline := b.LineBytes(bloc.Y)
		blineLen := util.CharacterCount(bline)

		leadingwsEnd := len(util.GetLeadingWhitespace(bline))
		trailingwsStart := blineLen - util.CharacterCount(util.GetTrailingWhitespace(bline))

		line, nColsBeforeStart, bslice, startStyle := w.getStartInfo(w.StartCol, bloc.Y)
		if startStyle != nil {
			curStyle = *startStyle
		}
		bloc.X = bslice

		// returns the rune to be drawn, style of it and if the bg should be preserved
		getRuneStyle := func(r rune, style tcell.Style, showoffset int, linex int, isplaceholder bool) (rune, tcell.Style, bool) {
			if nColsBeforeStart > 0 || vloc.Y < 0 || isplaceholder {
				return r, style, false
			}

			for _, mb := range matchingBraces {
				if mb.X == bloc.X && mb.Y == bloc.Y {
					if b.Settings["matchbracestyle"].(string) == "highlight" {
						if s, ok := config.Colorscheme["match-brace"]; ok {
							return r, s, false
						} else {
							return r, style.Reverse(true), false
						}
					} else {
						return r, style.Underline(true), false
					}
				}
			}

			if r != '\t' && r != ' ' {
				return r, style, false
			}

			var indentrunes []rune
			switch r {
			case '\t':
				if bloc.X < leadingwsEnd && indenttabchars != "" {
					indentrunes = []rune(indenttabchars)
				} else {
					indentrunes = []rune(tabchars)
				}
			case ' ':
				if linex%tabsize == 0 && bloc.X < leadingwsEnd && indentspacechars != "" {
					indentrunes = []rune(indentspacechars)
				} else {
					indentrunes = []rune(spacechars)
				}
			}

			var drawrune rune
			if showoffset < len(indentrunes) {
				drawrune = indentrunes[showoffset]
			} else {
				// use space if no showchars or after we showed showchars
				drawrune = ' '
			}

			if s, ok := config.Colorscheme["indent-char"]; ok {
				fg, _, _ := s.Decompose()
				style = style.Foreground(fg)
			}

			preservebg := false
			if b.Settings["hltaberrors"].(bool) && bloc.X < leadingwsEnd {
				if s, ok := config.Colorscheme["tab-error"]; ok {
					if b.Settings["tabstospaces"].(bool) && r == '\t' {
						fg, _, _ := s.Decompose()
						style = style.Background(fg)
						preservebg = true
					} else if !b.Settings["tabstospaces"].(bool) && r == ' ' {
						fg, _, _ := s.Decompose()
						style = style.Background(fg)
						preservebg = true
					}
				}
			}

			if b.Settings["hltrailingws"].(bool) {
				if s, ok := config.Colorscheme["trailingws"]; ok {
					if bloc.X >= trailingwsStart && bloc.X < blineLen {
						hl := true
						for _, c := range cursors {
							if c.NewTrailingWsY == bloc.Y {
								hl = false
								break
							}
						}
						if hl {
							fg, _, _ := s.Decompose()
							style = style.Background(fg)
							preservebg = true
						}
					}
				}
			}

			return drawrune, style, preservebg
		}

		draw := func(r rune, combc []rune, style tcell.Style, highlight bool, showcursor bool, preservebg bool) {
			defer func() {
				if nColsBeforeStart <= 0 {
					vloc.X++
				}
				nColsBeforeStart--
			}()

			if nColsBeforeStart > 0 || vloc.Y < 0 {
				return
			}

			if highlight {
				if w.Buf.HighlightSearch && w.Buf.SearchMatch(bloc) {
					style = config.DefStyle.Reverse(true)
					if s, ok := config.Colorscheme["hlsearch"]; ok {
						style = s
					}
				}

				_, origBg, _ := style.Decompose()
				_, defBg, _ := config.DefStyle.Decompose()

				// syntax or hlsearch highlighting with non-default background takes precedence
				// over cursor-line and color-column
				if !preservebg && origBg != defBg {
					preservebg = true
				}

				for _, c := range cursors {
					if c.HasSelection() &&
						(bloc.GreaterEqual(c.CurSelection[0]) && bloc.LessThan(c.CurSelection[1]) ||
							bloc.LessThan(c.CurSelection[0]) && bloc.GreaterEqual(c.CurSelection[1])) {
						// The current character is selected
						style = config.DefStyle.Reverse(true)

						if s, ok := config.Colorscheme["selection"]; ok {
							style = s
						}
					}

					if b.Settings["cursorline"].(bool) && w.active && !preservebg &&
						!c.HasSelection() && c.Y == bloc.Y {
						if s, ok := config.Colorscheme["cursor-line"]; ok {
							fg, _, _ := s.Decompose()
							style = style.Background(fg)
						}
					}
				}

				for _, m := range b.Messages {
					if bloc.GreaterEqual(m.Start) && bloc.LessThan(m.End) ||
						bloc.LessThan(m.End) && bloc.GreaterEqual(m.Start) {
						style = style.Underline(true)
						break
					}
				}

				if s, ok := config.Colorscheme["color-column"]; ok {
					if colorcolumn != 0 && vloc.X-w.gutterOffset+w.StartCol == colorcolumn && !preservebg {
						fg, _, _ := s.Decompose()
						style = style.Background(fg)
					}
				}
			}

			w.setCell(w.X+vloc.X, w.Y+vloc.Y, r, combc, style)

			if showcursor {
				for _, c := range cursors {
					if c.X == bloc.X && c.Y == bloc.Y && !c.HasSelection() {
						w.setShowCursor(w.X+vloc.X, w.Y+vloc.Y, c)
					}
				}
			}
		}

		wrap := func() {
			vloc.X = 0

			if vloc.Y >= 0 {
				if w.hasMessage {
					w.drawGutter(&vloc, &bloc)
				}
				if b.Settings["diffgutter"].(bool) {
					w.drawDiffGutter(lineNumStyle, true, &vloc, &bloc)
				}

				// This will draw an empty line number because the current line is wrapped
				if b.Settings["ruler"].(bool) {
					w.drawLineNum(lineNumStyle, true, &vloc, &bloc)
				}
			} else {
				vloc.X = w.gutterOffset
			}
		}

		type glyph struct {
			r     rune
			combc []rune
			style tcell.Style
			width int
		}

		var word []glyph
		if wordwrap {
			word = make([]glyph, 0, w.bufWidth)
		} else {
			word = make([]glyph, 0, 1)
		}
		wordwidth := 0

		totalwidth := w.StartCol - nColsBeforeStart
		for len(line) > 0 && vloc.X < maxWidth {
			r, combc, size := util.DecodeCharacter(line)
			line = line[size:]

			loc := buffer.Loc{X: bloc.X + len(word), Y: bloc.Y}
			curStyle, _ = w.getStyle(curStyle, loc)

			width := 0

			linex := totalwidth
			switch r {
			case '\t':
				ts := tabsize - (totalwidth % tabsize)
				width = util.Min(ts, maxWidth-vloc.X)
				totalwidth += ts
			default:
				width = runewidth.RuneWidth(r)
				// Only U+FE0F (VS16, emoji style) forces the base symbol to render as a 2-column emoji.
				// U+FE0E (VS15, text style) and other variation selectors keep the base width.
				for _, c := range combc {
					if c == 0xFE0F {
						width = 2
						break
					}
				}
				totalwidth += width
			}

			word = append(word, glyph{r, combc, curStyle, width})
			wordwidth += width

			// Collect a complete word to know its width.
			// If wordwrap is off, every single character is a complete "word".
			if wordwrap {
				if !util.IsWhitespace(r) && len(line) > 0 && wordwidth < w.bufWidth {
					continue
				}
			}

			// If a word (or just a wide rune) does not fit in the window
			if vloc.X+wordwidth > maxWidth && vloc.X > w.gutterOffset {
				for vloc.X < maxWidth {
					draw(' ', nil, config.DefStyle, false, false, true)
				}

				// We either stop or we wrap to draw the word in the next line
				if !softwrap {
					break
				} else {
					vloc.Y++
					if vloc.Y >= renderLimit {
						break
					}
					wrap()
				}
			}

			for _, r := range word {
				drawrune, drawstyle, preservebg := getRuneStyle(r.r, r.style, 0, linex, false)
				draw(drawrune, r.combc, drawstyle, true, true, preservebg)

				// Draw extra characters for tabs or wide runes
				for i := 1; i < r.width; i++ {
					if r.r == '\t' {
						drawrune, drawstyle, preservebg = getRuneStyle('\t', r.style, i, linex+i, false)
					} else {
						drawrune, drawstyle, preservebg = getRuneStyle(' ', r.style, i, linex+i, true)
					}
					draw(drawrune, nil, drawstyle, true, false, preservebg)
				}
				bloc.X++
			}

			word = word[:0]
			wordwidth = 0

			// If we reach the end of the window then we either stop or we wrap for softwrap
			if vloc.X >= maxWidth {
				if !softwrap {
					break
				} else {
					vloc.Y++
					if vloc.Y >= renderLimit {
						break
					}
					wrap()
				}
			}
		}

		style := config.DefStyle
		for _, c := range cursors {
			if b.Settings["cursorline"].(bool) && w.active &&
				!c.HasSelection() && c.Y == bloc.Y {
				if s, ok := config.Colorscheme["cursor-line"]; ok {
					fg, _, _ := s.Decompose()
					style = style.Background(fg)
				}
			}
		}
		for i := vloc.X; i < maxWidth; i++ {
			curStyle := style
			if s, ok := config.Colorscheme["color-column"]; ok {
				if colorcolumn != 0 && i-w.gutterOffset+w.StartCol == colorcolumn {
					fg, _, _ := s.Decompose()
					curStyle = style.Background(fg)
				}
			}
			w.setCell(i+w.X, vloc.Y+w.Y, ' ', nil, curStyle)
		}

		if vloc.X != maxWidth {
			// Display newline within a selection
			drawrune, drawstyle, preservebg := getRuneStyle(' ', config.DefStyle, 0, totalwidth, true)
			draw(drawrune, nil, drawstyle, true, true, preservebg)
		}

		// ★ 只写 sb.setRowMeta（方案B同源生成）
		// 不需要 rowOffset 分支：vloc.Y -= w.StartLine.Row 让 lineStartVY 可能为负，
		// screenRow - lineStartVY 天然从 StartLine.Row 开始
		// sb.setRowMeta 内部有 bounds 检查，跳过越界 screenRow
		for screenRow := lineStartVY; screenRow <= vloc.Y; screenRow++ {
			if screenRow >= 0 {
				w.sb.setRowMeta(screenRow, bloc.Y, screenRow-lineStartVY)
			}
		}

		bloc.X = w.StartCol
		bloc.Y++
		// ★ 末尾 break 条件改为 seg 边界
		if bloc.Y > seg.VisibleEnd {
			vloc.Y++ // 补上 for 循环被 break 跳过的自增
			break
		}
	}

	return vloc.Y
}

// displayBufferMD 是 displayBuffer 的 MD 渲染版本。
// 方案B：薄包装，调 displayToBuffer + showBuffer + updatePrevCursor。
// StartLine 已由上一轮 Relocate 算好；sb 失效则补渲染。
// 失效条件：
//   (1) 首帧 nil
//   (2) StartLine 不在 sb 覆盖区间
//   (3) overflow
//   (4) 宽度变化（resize / 切 ruler·diffgutter·scrollbar 改变 gutterOffset+bufWidth）
func (w *BufWindow) displayBufferMD(editMode bool) {
	if w.Height <= 0 || w.Width <= 0 {
		dbgLog("displayBufferMD: early return Height=%d Width=%d", w.Height, w.Width)
		return
	}
	w.ensureMDConfigReady()

	// 失效检查
	// ★ 用 coversExtent 而非 covers：covers 用 len(rows)（预分配容量=2×bufH）判断范围，
	//   但实际有效内容只写 nContent 行（常 < len(rows)）。鼠标滚动把 StartLine 推到 sb 中部时，
	//   covers(StartLine) 仍 true，但 viewport 尾部超出实际内容末尾 → 显示空白。
	needRender := w.sb == nil ||
		!w.sb.coversExtent(w.StartLine, w.bufHeight, w.Buf.LinesNum()) ||
		w.sb.overflow ||
		w.sb.width != w.gutterOffset+w.bufWidth ||
		w.sb.editMode != w.editMode
	dbgLog("=== displayBufferMD === editMode=%v StartLine={L:%d,R:%d} needRender=%v sbNil=%v",
		editMode, w.StartLine.Line, w.StartLine.Row, needRender, w.sb == nil)
	if needRender {
		w.displayToBuffer(w.StartLine, true) // Display 主路径：希望看到 cursor
	}

	w.showBuffer(w.StartLine)
	w.updatePrevCursor()
}

// filterSegmentsToVisible 从事件驱动算好的全 buffer segments 中，筛选当前可见区域。
// 非 MD 文件的 segs 为 nil，直接返回 nil（displayBufferMD 不被调用，原生路径处理）。
// 设置 VisibleStart/VisibleEnd 为 segment 在当前 viewport 中的实际可见范围，
// BufStartLine/BufEndLine 保持不变（完整数据传给 renderer）。
func filterSegmentsToVisible(segs []md.Segment, startY, endY int) []md.Segment {
	if segs == nil {
		return nil
	}
	var out []md.Segment
	for _, s := range segs {
		if s.BufEndLine < startY {
			continue // 完全在可视范围之上
		}
		if s.BufStartLine > endY {
			continue // 完全在可视范围之下
		}
		// 至少部分重叠，标记可见范围
		s.VisibleStart = s.BufStartLine
		if s.VisibleStart < startY {
			s.VisibleStart = startY
		}
		s.VisibleEnd = s.BufEndLine
		if s.VisibleEnd > endY {
			s.VisibleEnd = endY
		}
		out = append(out, s)
	}
	return out
}

// displayToBuffer 从 startLine 渲染到 screenBuffer，单一数据源。
// showCursor: 是否把光标段走 native（按 call site 固定取值）。
//
// 阶段3：主体实现（不接线，可单测）；displayBufferMD 尚未调用本函数。
func (w *BufWindow) displayToBuffer(startLine SLoc, showCursor bool) {
	b := w.Buf
	if w.Height <= 0 || w.Width <= 0 {
		dbgLog("displayToBuffer: early return Height=%d Width=%d", w.Height, w.Width)
		return
	}
	w.ensureMDConfigReady()

	bufWidth, bufHeight := w.bufWidth, w.bufHeight
	cap := 2 * bufHeight // 2× 吸收跨段展开增量
	dbgLog(">> displayToBuffer ENTER startLine={L:%d,R:%d} showCursor=%v bufH=%d bufW=%d gutterOff=%d cap=%d prevCursorY=%d editMode=%v",
		startLine.Line, startLine.Row, showCursor, bufHeight, bufWidth, w.gutterOffset, cap, w.prevCursorY, w.editMode)

	// 初始化 screenBuffer（2× 容量，行宽含 gutter）
	if w.sb == nil {
		w.sb = &screenBuffer{}
	}
	w.sb.reset(cap, w.gutterOffset+bufWidth, w.X, w.Y)
	w.sb.startLine = startLine
	w.sb.editMode = w.editMode

	// 切 sink（renderer 和 gutter 走 screenBuffer）
	w.sink = w.sb
	defer func() { w.sink = nil }() // 防御性复位（sink=nil 时 setCell 走真屏）

	// 可见段（沿用现状 filter）
	visibleStart := startLine.Line
	visibleEnd := visibleStart + cap
	if visibleEnd >= b.LinesNum() {
		visibleEnd = b.LinesNum() - 1
	}
	if visibleStart >= b.LinesNum() {
		return // buffer 末尾后，无内容
	}
	segments := filterSegmentsToVisible(b.MDSegments, visibleStart, visibleEnd)
	dbgLog("   displayToBuffer: LinesNum=%d visibleStart=%d visibleEnd=%d filteredSegs=%d totalSegs=%d",
		b.LinesNum(), visibleStart, visibleEnd, len(segments), len(b.MDSegments))
	cursors := b.GetCursors()

	// ★ cursor 可见性预判：若 cursor 不在本批渲染范围 [visibleStart, visibleEnd] 内，
	//   则把 showCursor 当 false 处理。否则 g2 停止条件（showCursor && !cursorShowed）
	//   永远无法满足（cursor 段不在 visible → cursorShowed 恒 false），
	//   渲染循环一路跑到 vY>=cap → overflow=true → showBuffer fallback 到原生 displayBuffer()，
	//   用户看到原始 markdown 格式（鼠标滚动把 cursor 推出 viewport 时触发）。
	ac := b.GetActiveCursor()
	if showCursor && ac != nil && (ac.Y < visibleStart || ac.Y > visibleEnd) {
		showCursor = false
		dbgLog("   displayToBuffer: cursor Y=%d 不在 visible[%d..%d] → showCursor=false (避免 overflow fallback)", ac.Y, visibleStart, visibleEnd)
	}

	// 旧光标段（跨段切换时，旧段从 native 切回 MD，装饰行必须整段渲染）
	oldSeg := w.findSegmentContaining(w.prevCursorY)
	// M1 修复：oldSeg 不在本次渲染窗口即视为完成。
	// PageDown 把 StartLine 推到 oldSeg 之下后按 ↓（case A），oldSeg 整体在 startLine 之上，
	// 循环永远遇不到 oldSeg → 三目的凑不齐 → 一路渲染到 overflow。
	scrollmargin := int(b.Settings["scrollmargin"].(float64))
	oldSegDone := oldSeg == nil ||
		oldSeg.BufEndLine < startLine.Line ||
		oldSeg.BufStartLine > startLine.Line+bufHeight+scrollmargin
	// oldSeg 用 BufStartLine 作 key 与迭代段比较（range 返回副本，无法比指针）
	oldSegKey := -1
	if oldSeg != nil {
		oldSegKey = oldSeg.BufStartLine
	}

	cursorShowed := false

	vY := 0
	iter := 0
	for _, seg := range segments {
		iter++
		// 兜底：2×bufHeight 强制退出
		if vY >= cap {
			w.sb.overflow = true
			dbgLog("   [iter%d] BREAK: vY=%d >= cap=%d → overflow", iter, vY, cap)
			break
		}

		curY := -1
		if ac := b.GetActiveCursor(); ac != nil {
			curY = ac.Y
		}
		dbgLog("   [iter%d] top vY=%d seg=[L%d..L%d] vis=[%d..%d] cursorY=%d editMode=%v", iter, vY, seg.BufStartLine, seg.BufEndLine, seg.VisibleStart, seg.VisibleEnd, curY, w.editMode)

		vYBefore := vY
		// ★ 同源写入 line/segRow + cells（renderer 内部完成）
		if showCursor && w.editMode && hasCursorInside(seg, cursors) {
			vY = w.renderSegmentNative(seg, vY)
			dbgLog("   [iter%d] renderSegmentNative → vY %d→%d (Δ=%d)", iter, vYBefore, vY, vY-vYBefore)
		} else {
			vY = w.renderSegmentMD(seg, vY)
			dbgLog("   [iter%d] renderSegmentMD → vY %d→%d (Δ=%d)", iter, vYBefore, vY, vY-vYBefore)
		}
		// ★ 光标段渲染完成即满足 g2（无论走 native 还是 MD）。
		//   ESC 阅读模式下 cursor 段走 MD，此处置位避免 overflow 兜底误触发整屏原生回退。
		if showCursor && hasCursorInside(seg, cursors) {
			cursorShowed = true
		}
		if seg.BufStartLine == oldSegKey {
			oldSegDone = true
		}

		// 三目的停止条件（忠于需求文档伪代码）
		canBreak := true
		g1, g2, g3 := true, true, true
		// 目的1：旧光标段必须完整渲染（跨段切换，旧段装饰行不能截断）
		if !oldSegDone {
			canBreak = false
			g1 = false
		}
		// 目的2（showCursor=true）：cursor 段必须已渲染
		if showCursor && !cursorShowed {
			canBreak = false
			g2 = false
		}
		// 目的3：必须渲染到 viewport 底边以下，留足 Relocate 微调余量
		if vY < bufHeight+scrollmargin {
			canBreak = false
			g3 = false
		}
		dbgLog("   [iter%d] canBreak=%v (g1oldSegDone=%v g2cursorShowed=%v g3vY<bufH+margin: %d<%d=%v) oldSeg=%v",
			iter, canBreak, g1, g2, vY, bufHeight+scrollmargin, g3, segRangeStr(oldSeg))
		if canBreak {
			break
		}
	}
	dbgLog("<< displayToBuffer EXIT loop ended at vY=%d overflow=%v oldSegDone=%v cursorShowed=%v",
		vY, w.sb.overflow, oldSegDone, cursorShowed)
	// 统计实际写入的行（line != -2 的）
	written := 0
	firstLine, lastLine := -999, -999
	for _, r := range w.sb.rows {
		if r.line != -2 {
			written++
			if firstLine == -999 {
				firstLine = r.line
			}
			lastLine = r.line
		}
	}
	dbgLog("   displayToBuffer: sb.rows total=%d written(content)=%d firstLine=%d lastLine=%d",
		len(w.sb.rows), written, firstLine, lastLine)
	w.sb.nContent = written // ★ 记录有效内容行数，供 coversExtent 判断 viewport 尾部是否超出
	w.sb.lastLine = lastLine // ★ 记录实际渲染到的最后 buffer 行，供 coversExtent 的 buffer-end 例外判断
	// 剩余不填满 —— screenBuffer 只渲染实际内容，showBuffer 负责尾部空白填充
}

// showBuffer 把 screenBuffer 从 startLine 起的一段 blit 到真屏。
// 职责单一：copy + 边界处理，不做 dirty 判定。
func (w *BufWindow) showBuffer(startLine SLoc) {
	bufHeight := w.bufHeight
	bufWidth := w.bufWidth
	dbgLog(">> showBuffer ENTER startLine={L:%d,R:%d} bufH=%d bufW=%d gutterOff=%d",
		startLine.Line, startLine.Row, bufHeight, bufWidth, w.gutterOffset)

	// overflow 兜底：走原生 displayBuffer
	if w.sb.overflow {
		dbgLog("   showBuffer: OVERFLOW → displayBuffer() fallback")
		// ⚠️ 已知限制：对 .md 会画带 # / | 的原始字符（仅单 segment > 2×bufHeight 触发）
		w.displayBuffer()
		return
	}

	// 范围检查：startLine 超出 sb 覆盖区间，或宽度变化（resize / 切 ruler 等）→ 补渲染
	// ★ 用 coversExtent（屏幕行精确判断）而非旧 covers（len(rows)=2×bufH），
	//   与 displayBufferMD 的 needRender 判断保持一致
	covers := w.sb.coversExtent(startLine, bufHeight, w.Buf.LinesNum())
	widthOK := w.sb.width == w.gutterOffset+bufWidth
	dbgLog("   showBuffer: sb.startLine={L:%d,R:%d} sb.width=%d len(rows)=%d covers=%v widthOK=%v",
		w.sb.startLine.Line, w.sb.startLine.Row, w.sb.width, len(w.sb.rows), covers, widthOK)
	if !covers || !widthOK {
		dbgLog("   showBuffer: 补渲染 displayToBuffer(showCursor=false)")
		w.displayToBuffer(startLine, false) // 滚动场景：showCursor=false
	}

	// 找 startLine 在 sb.rows 中的起点 vY
	startVY, ok := w.sb.rowIndexOf(startLine)
	dbgLog("   showBuffer: rowIndexOf → startVY=%d ok=%v", startVY, ok)
	if !ok {
		// startLine 落装饰行/边界：向下找首个内容行
		startVY, ok = w.sb.rowIndexNearest(startLine)
		dbgLog("   showBuffer: rowIndexNearest → startVY=%d ok=%v", startVY, ok)
		if !ok {
			dbgLog("   showBuffer: rowIndexNearest 失败 → return（不画任何内容！）")
			return
		}
	}
	w.sb.blitStart = startVY // 记录，供点击映射

	// blit，处理尾部不足
	endVY := startVY + bufHeight
	if endVY > len(w.sb.rows) {
		endVY = len(w.sb.rows)
	}
	blitCount := endVY - startVY
	dbgLog("   showBuffer: BLIT startVY=%d endVY=%d rows=%d bufH=%d → 将画 %d 行内容（其余尾部空白）",
		startVY, endVY, len(w.sb.rows), bufHeight, blitCount)
	if blitCount <= 1 {
		dbgLog("   ⚠⚠⚠ showBuffer: blitCount=%d 极少！前 10 行 rows.line:", blitCount)
		for i := 0; i < 10 && i < len(w.sb.rows); i++ {
			dbgLog("      rows[%d].line=%d segRow=%d", i, w.sb.rows[i].line, w.sb.rows[i].segRow)
		}
	}

	defStyle := config.DefStyle
	for vY := 0; vY < bufHeight; vY++ {
		srcRow := startVY + vY
		if srcRow >= endVY {
			// 尾部空白填充
			w.drawGutterAndLineNumMD(vY, -1, false)
			for col := 0; col < bufWidth; col++ {
				w.setCell(w.X+w.gutterOffset+col, w.Y+vY, ' ', nil, defStyle)
			}
			continue
		}
		row := &w.sb.rows[srcRow]
		// 整行 blit（gutter + content）
		// 防御性边界：cells 长度与 gutterOffset+bufWidth 不一致时只 blit 实际存在的 cell
		blitW := w.gutterOffset + bufWidth
		for col := 0; col < blitW; col++ {
			if col >= len(row.cells) {
				w.setCell(w.X+col, w.Y+vY, ' ', nil, defStyle)
				continue
			}
			c := row.cells[col]
			w.setCell(w.X+col, w.Y+vY, c.r, c.combc, c.style)
		}
	}

	// 同步光标位置到 terminal
	// ★ 每个 sbCursor.screenY 是 sb 本地行号（从 sb.startLine 算起），
	//   但可见窗口从 startVY 开始 blit，必须减去 startVY 才是屏幕行。
	//   不减会导致光标画低 startVY 行（scrollup 后光标错位 / 消失到 statusLine 下）。
	//   遍历本帧记录的所有 cursor（主+次），主光标走真终端光标、次级走 fake cursor。
	//   同位置多次调用 ShowFakeCursorMulti 无副作用（幂等），无需去重。
	for _, sc := range w.sb.cursors {
		curScreenY := sc.screenY - startVY
		if curScreenY >= 0 && curScreenY < bufHeight {
			w.showCursor(w.X+sc.screenX, w.Y+curScreenY, sc.c.Num == 0)
		}
	}

	// 注意：cells 不置 nil。Plan §3.1 推荐“showBuffer blit 后置 nil”以释放内存，
	// 但 §4.4 的“skip displayToBuffer”优化依赖 sb.cells 仍可 blit。
	// StartLine 不变时（如 idle 、同段内移动）跳过 displayToBuffer，若此时 cells=nil
	// 则下一帧 showBuffer 会画空。安全策略：保持 cells 存活，依赖 reset 复用底层数组的 GC
	// 压力优化（§3.1 reset 设计）仅在 displayToBuffer 入口触发。
}

// computeRowCounts 已删除，使用 computeVisibleRowCounts 替代。

// drawGutterAndLineNumMD 在指定 screen 行绘制 gutter 和行号。
// bufLine 为 -1 表示空行/装饰行。
// softwrapped=true 表示该行是 wrap 续行，行号留空但 diff 标志照画。
func (w *BufWindow) drawGutterAndLineNumMD(vY int, bufLine int, softwrapped bool) {
	b := w.Buf
	vloc := buffer.Loc{X: 0, Y: vY}
	bloc := buffer.Loc{X: 0, Y: bufLine}

	lineNumStyle := config.DefStyle
	if style, ok := config.Colorscheme["line-number"]; ok {
		lineNumStyle = style
	}

	if w.hasMessage && bufLine >= 0 {
		w.drawGutter(&vloc, &bloc)
	}
	if b.Settings["diffgutter"].(bool) && bufLine >= 0 {
		w.drawDiffGutter(lineNumStyle, softwrapped, &vloc, &bloc)
	}
	if b.Settings["ruler"].(bool) {
		if bufLine >= 0 && !softwrapped {
			w.drawLineNum(lineNumStyle, false, &vloc, &bloc)
		} else {
			// 续行/空行/装饰行：行号位留空
			for vloc.X < w.gutterOffset {
				w.setCell(w.X+vloc.X, w.Y+vY, ' ', nil, lineNumStyle)
				vloc.X++
			}
		}
	}
}

// expandLineStyles 将稀疏 Match map 展开为稠密 style 数组。
// result[i] = 第 i 个 rune 经过 highlighter + colorscheme 之后的完整 style。
// 用于 renderSegmentMD：预计算稠密数组后，按 BufX 直接查颜色，解决标记隐藏后的锚点丢失问题。
// isCodeBlock 为 true 时跳过污染检测：块内 Match 本就是嵌入语言组（constant.string 等），
// polluted 会恒真，跳过才能让嵌入语言高亮正常工作。
func (w *BufWindow) expandLineStyles(bufLine int, runeCount int, baseStyle tcell.Style, isCodeBlock bool) []tcell.Style {
	charStyles := make([]tcell.Style, runeCount)
	match := w.Buf.Match(bufLine)
	// 非 codeblock 段的污染行（跨行 region state 粘滞）用 fresh 单行重高亮替换。
	if !isCodeBlock && polluted(match) {
		match = freshLineMatch(w.Buf.SyntaxDef, string(w.Buf.LineBytes(bufLine)))
	}
	curStyle := baseStyle
	for i := 0; i < runeCount; i++ {
		if group, ok := match[i]; ok {
			curStyle = config.GetColor(group.String())
		}
		charStyles[i] = curStyle
	}

	return charStyles
}

// polluted 判断该行 Match 是否被跨行 region 粘滞污染：
// 存在任何「非空且不带 md- 前缀」的组即污染。
// Group 名为空（默认组）是干净行常有的中性组，不算污染。
func polluted(m highlight.LineMatch) bool {
	for _, g := range m {
		name := g.String()
		if name != "" && !strings.HasPrefix(name, "md-") {
			return true
		}
	}
	return false
}

// freshLineMatch 对单行做无状态重高亮，绕开 buffer 里粘滞的跨行 region state。
// 每次新建一次性 Highlighter：那是可变对象，HighlightString 会读写 lastRegion，
// 复用共享 Highlighter 会引入显示线程与编辑线程的竞态；单行从 nil 态开始，结果确定。
// 不挂 BufWindow，便于独立单测。
func freshLineMatch(def *highlight.Def, line string) highlight.LineMatch {
	if def == nil {
		return nil
	}
	h := highlight.NewHighlighter(def)
	matches := h.HighlightString(line)
	if len(matches) == 0 {
		return nil
	}
	return matches[0]
}

// ScreenRowToLine 将屏幕行偏移（相对 viewport 顶部）映射为 buffer 行号。
// 方案B：从 sb.rows[sb.blitStart + screenOffset] 查 line。
// 装饰行（-1）向下映射到紧邻的内容行（符合从上往下读的直觉）；若下方已无内容行
// （装饰行位于 sb 渲染末尾，如文件尾的表格底框），则映射到文件 last line。
// 空白区域（-2，sb 尾部预填充）真正无内容，返回 (0, false) 回退原生 Scroll 逻辑。
// 返回 (bufferLine, true) 表示成功映射，(0, false) 表示应回退原始 Scroll 逻辑。
func (w *BufWindow) ScreenRowToLine(screenOffset int) (int, bool) {
	if w.sb == nil {
		return 0, false
	}
	idx := w.sb.blitStart + screenOffset
	if screenOffset < 0 || idx >= len(w.sb.rows) {
		return 0, false
	}
	if w.sb.rows[idx].line >= 0 {
		return w.sb.rows[idx].line, true
	}
	if w.sb.rows[idx].line == -1 {
		// 装饰行（表格框/标题下划线/代码块边框）：向下找首个内容行
		for i := idx + 1; i < len(w.sb.rows); i++ {
			if w.sb.rows[i].line >= 0 {
				return w.sb.rows[i].line, true
			}
		}
		// 装饰行下方无内容行（装饰行在 sb 渲染末尾）→ 定位到文件 last line
		return w.Buf.LinesNum() - 1, true
	}
	// 空白填充（-2，sb 尾部预分配）：真正无内容，回退原生 Scroll
	return 0, false
}

// LineToScreenRow 将 (bufferLine, segmentRow) 二元组精确匹配为屏行。
// 方案B：从 sb.rows 扫，O(2×bufHeight)。
func (w *BufWindow) LineToScreenRow(line, row int) (int, bool) {
	if w.sb == nil {
		return 0, false
	}
	for i, r := range w.sb.rows {
		if r.line == line && r.segRow == row {
			return i, true
		}
	}
	return 0, false
}

// relocateVerticalMD 是 Relocate 的 MD 垂直滚动分支。
// 方案B：入口 displayToBuffer 拿 fresh sb（唯一一次渲染），随后查询微调。
// case A（方向键）与 case C（goto/search/pageup/pagedown）统一逻辑，
// 唯一区别是渲染起点选择（覆盖上帧 sb 则用上帧起点 = case A，否则 1:1 估算 = case C）。
func (w *BufWindow) relocateVerticalMD(c SLoc, scrollmargin, height int) bool {
	dbgLog(">>> relocateVerticalMD ENTER c={L:%d,R:%d} curStartLine={L:%d,R:%d} scrollmargin=%d height=%d",
		c.Line, c.Row, w.StartLine.Line, w.StartLine.Row, scrollmargin, height)

	// 1. 估算渲染起点（cheap 预判，渲染前）
	var displayStart SLoc
	caseLabel := "A"
	// case A：cursor 在上一帧 sb 的第一屏内（连续导航，StartLine 仍有效）
	// case C：jump 到远处（旧 sb 虽覆盖 cursor 行但 StartLine 已失效）→ 重估 displayStart
	//   区分依据：cursor 在旧 sb 的 row。连续 ↓ 时 cursor 在 botMarginRow 附近
	//   （row < height，第一屏内）；jump 时 cursor 跳到旧 sb 第二屏（row >= height）。
	if w.sb != nil && w.sb.coversLine(c.Line) {
		curRow, ok := w.sb.rowIndexOf(c)
		// ★ Bug fix: 回车新增行 / 连续 ↓ 场景。cursor 行是 buffer 刚扩展的新行，
		//   sb（上一帧）尚未渲染到 → rowIndexOf 必失败。
		//   当 c.Line 紧贴 sb.lastLine 之后（c.Line == sb.lastLine+1），
		//   估算 curRow = lastRow + 1，仍按 case A 判断可见视口，
		//   避免误判 case C 导致屏幕跳到 c.Line - scrollmargin。
		if !ok && w.sb.lastLine >= 0 && c.Line == w.sb.lastLine+1 {
			if lastRow, lastOk := w.sb.rowIndexOf(SLoc{Line: w.sb.lastLine, Row: 0}); lastOk {
				curRow = lastRow + 1
				ok = true
			}
		}
		// ★ Bug fix（软换行误 scrollup）：行内编辑导致 wrap 行数变化（如同 buffer line
		//   变长后 softwrap 多出一行）。cursor 还在同一 buffer line，但 Row 超出 old sb
		//   记录的最大 segRow → rowIndexOf({line, newRow}) 失败。这是连续编辑的小位移
		//   （视觉行只增加几行），应走 case A 保持起点，绝不能误判 case C 跳远，否则
		//   StartLine 会被重置成 c.Line - scrollmargin，光标被强行贴到 viewport 顶部。
		//   old sb 中 line 的最后一行索引即为 cursor 的近似位置（新 Row 会让光标再下移几行，
		//   真实 cursorRow 由 displayToBuffer 之后的 fresh sb 重新校正，这里无需精确）。
		if !ok {
			if idx, found := w.sb.lastRowIndexOf(c.Line); found {
				curRow = idx
				ok = true
			}
		}
		// ★ 用可见视口 [startVY, startVY+height] 判断（左闭右闭），而非 sb 绝对第一屏 [0, height)。
		//   scrollup 后 StartLine 在 sb 内部推进，blit startVY>0，
		//   可见窗口随之上移，[0, height) 不再代表可见区。
		//   右端用 <= 而非 <：cursor 刚好越出底部 1 行（如点击屏幕最底装饰行，
		//   ScreenRowToLine 向下映射到紧邻内容行）时，应走 case A 小幅 scrollup，
		//   而非 case C 跳跃（Bug #8）。
		startVY, startOk := w.sb.rowIndexOf(w.StartLine)
		dbgLog("    relocate: caseJudge curRow=%d startVY=%d(startOk=%v) visibleWin=[%d,%d] height=%d",
			curRow, startVY, startOk, startVY, startVY+height, height)
		if ok && startOk && curRow >= startVY && curRow <= startVY+height {
			displayStart = w.StartLine // case A：cursor 在可见视口内（含刚越出底部 1 行）
		} else {
			// case C：jump 到远处，重估 displayStart（对齐 micro 原生分支 3/4）
			bEnd := w.SLocFromLoc(w.Buf.End())
			if c.Line <= bEnd.Line-scrollmargin {
				// 分支 3：cursor 在中间区域 → 粗估放到 botMarginRow 附近
				//   （精确位置由渲染后微调阶段的 SCROLLUP 校正）
				displayStart = SLoc{Line: c.Line - scrollmargin, Row: 0}
				dbgLog("    relocate: caseC-branch3 displayStart=%d (c=%d bEnd=%d margin=%d)",
					displayStart.Line, c.Line, bEnd.Line, scrollmargin)
			} else {
				// 分支 4：cursor 在末尾区域（短 buffer / paste / goto 末尾）
				//   → end-pin：bEnd 钉到视口底，避免顶部留白（Bug #9 修复）
				displayStart = SLoc{Line: bEnd.Line - height + 1, Row: 0}
				dbgLog("    relocate: caseC-branch4 end-pin displayStart=%d (c=%d bEnd=%d height=%d)",
					displayStart.Line, c.Line, bEnd.Line, height)
			}
			caseLabel = "C"
			w.StartLine = displayStart
		}
	} else {
		// case C：jump 到远处，重估 displayStart（对齐 micro 原生分支 3/4）
		bEnd := w.SLocFromLoc(w.Buf.End())
		if c.Line <= bEnd.Line-scrollmargin {
			// 分支 3：cursor 在中间区域 → 粗估放到 botMarginRow 附近
			//   （精确位置由渲染后微调阶段的 SCROLLUP 校正）
			displayStart = SLoc{Line: c.Line - scrollmargin, Row: 0}
			dbgLog("    relocate: caseC-branch3 displayStart=%d (c=%d bEnd=%d margin=%d)",
				displayStart.Line, c.Line, bEnd.Line, scrollmargin)
		} else {
			// 分支 4：cursor 在末尾区域（短 buffer / paste / goto 末尾）
			//   → end-pin：bEnd 钉到视口底，避免顶部留白（Bug #9 修复）
			displayStart = SLoc{Line: bEnd.Line - height + 1, Row: 0}
			dbgLog("    relocate: caseC-branch4 end-pin displayStart=%d (c=%d bEnd=%d height=%d)",
				displayStart.Line, c.Line, bEnd.Line, height)
		}
		caseLabel = "C"
		w.StartLine = displayStart
	}
	if displayStart.Line < 0 {
		displayStart.Line = 0
	}
	dbgLog("    relocate: case=%s displayStart={L:%d,R:%d}", caseLabel, displayStart.Line, displayStart.Row)

	// 2. 唯一一次渲染（showCursor=true：导航场景，希望看到 cursor 行）
	w.displayToBuffer(displayStart, true)

	// 3. 查询微调（非渲染）：让 cursor 落在期望 margin
	if w.sb.overflow {
		dbgLog("    relocate: overflow=true → fallback")
		return w.relocateVerticalNativeFallback(c, scrollmargin, height) // 极端兑底
	}
	cursorRow, ok := w.sb.rowIndexOf(c)
	if !ok {
		dbgLog("    relocate: rowIndexOf({L:%d,R:%d})=nil → fallback", c.Line, c.Row)
		return w.relocateVerticalNativeFallback(c, scrollmargin, height)
	}
	botMarginRow := height - 1 - scrollmargin
	dbgLog("    relocate: cursorRow=%d botMarginRow=%d (sbRows=%d)", cursorRow, botMarginRow, len(w.sb.rows))
	if cursorRow > botMarginRow {
		delta := cursorRow - botMarginRow
		loc, ok := w.sb.slocAt(delta)
		dbgLog("    relocate: SCROLLUP branch delta=%d slocAt(%d)={L:%d,R:%d} ok=%v", delta, delta, loc.Line, loc.Row, ok)
		if !ok || loc.Line < 0 {
			// delta 落装饰行：向下找首个内容行
			for d := delta + 1; d < len(w.sb.rows); d++ {
				if l, ok := w.sb.slocAt(d); ok && l.Line >= 0 {
					loc = l
					dbgLog("    relocate: decor-skip → rows[%d]={L:%d,R:%d}", d, loc.Line, loc.Row)
					break
				}
			}
			if loc.Line < 0 {
				dbgLog("    relocate: decor-skip failed → fallback")
				return w.relocateVerticalNativeFallback(c, scrollmargin, height)
			}
		}
		w.StartLine = loc
		dbgLog("<<< relocateVerticalMD EXIT scrollup newStartLine={L:%d,R:%d} (was {L:%d,R:%d}, delta=%d)",
			w.StartLine.Line, w.StartLine.Row, displayStart.Line, displayStart.Row, delta)
		return true
	}
	if cursorRow < scrollmargin {
		w.StartLine = w.Scroll(c, -scrollmargin) // 向上：段内 1:1（沿用现状）
		dbgLog("<<< relocateVerticalMD EXIT scrolldown newStartLine={L:%d,R:%d}", w.StartLine.Line, w.StartLine.Row)
		return true
	}
	dbgLog("<<< relocateVerticalMD EXIT nochange (case=%s cursorRow=%d in [%d,%d] startLine={L:%d,R:%d})",
		caseLabel, cursorRow, scrollmargin, botMarginRow, w.StartLine.Line, w.StartLine.Row)
	return true
}

// relocateVerticalNativeFallback 复刻 micro 原生垂直 Relocate（1:1 假设），
// 供 MD 路径边界场景兜底。
//
// ⚠️ 此函数需与 bufwindow.go 中 Relocate 的非 MD 分支保持同步。
// micro 的 Relocate 垂直逻辑历史稳定，但修改其 else 分支时务必同步本函数。
func (w *BufWindow) relocateVerticalNativeFallback(c SLoc, scrollmargin, height int) bool {
	bStart := SLoc{0, 0}
	bEnd := w.SLocFromLoc(w.Buf.End())
	ret := false
	if c.LessThan(w.Scroll(w.StartLine, scrollmargin)) && c.GreaterThan(w.Scroll(bStart, scrollmargin-1)) {
		w.StartLine = w.Scroll(c, -scrollmargin)
		ret = true
	} else if c.LessThan(w.StartLine) {
		w.StartLine = c
		ret = true
	}
	if c.GreaterThan(w.Scroll(w.StartLine, height-1-scrollmargin)) && c.LessEqual(w.Scroll(bEnd, -scrollmargin)) {
		w.StartLine = w.Scroll(c, -height+1+scrollmargin)
		ret = true
	} else if c.GreaterThan(w.Scroll(bEnd, -scrollmargin)) && c.GreaterThan(w.Scroll(w.StartLine, height-1)) {
		w.StartLine = w.Scroll(bEnd, -height+1)
		ret = true
	}
	return ret
}

// ============================================================================
// MicroNeo (方案B): screenBuffer 数据结构 + cellSink 接口
// 阶段1：基础设施。本阶段不修改现有行为，只新增类型与方法。
// 编译目标：make build-quick 通过；非 MD 文件 sink 恒为 realScreenSink，行为不变。
// ============================================================================

// mdCell 是 screenBuffer 里一个显示单元（rune + 合字 + 样式）。
type mdCell struct {
	r     rune
	combc []rune
	style tcell.Style
}

// screenRow 是 screenBuffer 的一行：line 元数据 + cells 显示数据【同源生成】。
// cells 覆盖整行宽度（gutterOffset + bufWidth），gutter 区也纳入，供 showBuffer 整行 blit。
type screenRow struct {
	line   int      // 对应 buffer 行（绝对）；-1=装饰行，-2=空白填充。供查询
	segRow int      // softwrap 段号（同 SLoc.Row）。供查询
	cells  []mdCell // 显示数据；showBuffer blit 后置 nil 释放
}

// sbCursor 记录一个光标：复用 micro 原生 *buffer.Cursor（含 Num，blit 时算 main），
// 外加 sb 本地屏幕坐标（离屏渲染必需）。不自己造 main 字段，与原生就地画法保持一致。
type sbCursor struct {
	c       *buffer.Cursor // 复用 micro 原生 cursor（c.Num==0 即主光标）
	screenX int            // sb 本地 X（已减 originX）
	screenY int            // sb 本地 Y（已减 originY），= sb.rows 索引
}

// screenBuffer 是 MD 渲染的离屏单一数据源：既是渲染结果，又是 line↔vY 查询地图。
type screenBuffer struct {
	rows      []screenRow // 长度动态，≤ 2*bufHeight
	startLine SLoc        // 本批 rows 渲染时的起点（case A/C 范围检查用）
	nContent  int         // 实际有效内容行数（rows 中 line!=-2 的行数）；coversExtent 用它而非 len(rows)
	lastLine  int         // 实际渲染到的最后一个 buffer 行号（coversExtent 的 buffer-end 例外判断用）
	blitStart int         // 上次 showBuffer blit 的 rows 起始下标（点击映射用）
	originX   int         // 渲染窗口原点 X（= w.X），绝对坐标→本地坐标转换
	originY   int         // 渲染窗口原点 Y（= w.Y）
	width     int         // 每行 cell 宽度（= gutterOffset + bufWidth）
	overflow  bool        // 2×bufHeight 兜底触发标记（→ fallback）
	cursors   []sbCursor  // 本批渲染落在可见区的所有光标（含主 + 次）
	editMode  bool // 本次 render 时的 editMode，供 displayBufferMD 跨帧比较
}

// cellSink 是渲染目标抽象。SetContent 签名与 screen.SetContent 对齐（绝对坐标）。
type cellSink interface {
	SetContent(x, y int, mainc rune, combc []rune, style tcell.Style)
}

// realScreenSink 包装 screen.SetContent，原生路径行为字节级不变。
type realScreenSink struct{}

func (realScreenSink) SetContent(x, y int, mainc rune, combc []rune, style tcell.Style) {
	screen.SetContent(x, y, mainc, combc, style)
}

// SetContent 是 screenBuffer 的 sink 实现：绝对坐标 → 本地 (row, cell)。
// 越界丢弃（screenBuffer 是 2× cap，渲染可能溢出尾部）。
func (s *screenBuffer) SetContent(x, y int, mainc rune, combc []rune, style tcell.Style) {
	if s == nil {
		return
	}
	row := y - s.originY
	col := x - s.originX
	if row < 0 || row >= len(s.rows) || col < 0 || col >= s.width {
		return
	}
	rowData := &s.rows[row]
	if col >= len(rowData.cells) {
		// cells 长度不足（width 改变后旧 cells 复用）：忽略本 cell
		// displayToBuffer 入口 reset 会保证 cells 长度匹配
		return
	}
	rowData.cells[col] = mdCell{r: mainc, combc: combc, style: style}
}

// ShowCursor 记录光标到 cursors 切片（cells 坐标系）。
func (s *screenBuffer) ShowCursor(x, y int, c *buffer.Cursor) {
	if s == nil {
		return
	}
	s.cursors = append(s.cursors, sbCursor{
		c:       c,
		screenX: x - s.originX,
		screenY: y - s.originY,
	})
}

// reset 重置 screenBuffer；复用底层 slice，仅在容量/宽度变化时重建。
func (s *screenBuffer) reset(capacity, width, ox, oy int) {
	s.originX = ox
	s.originY = oy
	s.width = width
	s.overflow = false
	s.cursors = s.cursors[:0]
	s.blitStart = 0
	s.nContent = 0

	// 容量/宽度变化 → 重建 rows
	if cap(s.rows) < capacity || s.width != width {
		s.rows = make([]screenRow, capacity)
		for i := range s.rows {
			s.rows[i].cells = make([]mdCell, width)
			s.rows[i].line = -2
			s.rows[i].segRow = -1
		}
		return
	}

	// 复用：截短到 capacity 并清零元数据 + cells
	s.rows = s.rows[:capacity]
	for i := range s.rows {
		s.rows[i].line = -2
		s.rows[i].segRow = -1
		// cells 必须长度匹配；宽度变化时上面已重建
		if len(s.rows[i].cells) < width {
			s.rows[i].cells = make([]mdCell, width)
		} else {
			// 复用底层：清零 cells 内容
			cells := s.rows[i].cells[:width]
			for j := range cells {
				cells[j] = mdCell{}
			}
			s.rows[i].cells = cells
		}
	}
}

// setRowMeta 写入某行的 line/segRow 元数据（同源填充，不写 cells）。
func (s *screenBuffer) setRowMeta(vY, line, segRow int) {
	if s == nil || vY < 0 || vY >= len(s.rows) {
		return
	}
	s.rows[vY].line = line
	s.rows[vY].segRow = segRow
}

// covers 判定 startLine 是否在 screenBuffer 的内容行覆盖范围内。
// 范围 = [startLine.Line, startLine.Line + capacity)。
func (s *screenBuffer) covers(sl SLoc) bool {
	if s == nil || len(s.rows) == 0 {
		return false
	}
	return sl.Line >= s.startLine.Line && sl.Line < s.startLine.Line+len(s.rows)
}

// coversLine 判定 buffer line 是否在 screenBuffer 的覆盖范围内。
func (s *screenBuffer) coversLine(line int) bool {
	if s == nil || len(s.rows) == 0 {
		return false
	}
	return line >= s.startLine.Line && line < s.startLine.Line+len(s.rows)
}

// coversExtent 判定从 sl 开始的 height 行（屏幕行）是否都在 sb 有效内容内。
// 与 covers/coversLine 的区别：
//   - 用 nContent（实际写入行数）而非 len(rows)（预分配容量）
//   - 用屏幕行精确判断（rowIndexOf(sl)+height <= nContent），而非 buffer 行估算
//     （MD 表格/代码块展开后，buffer 行与屏幕行非 1:1，buffer 行判断会误判）
//   - buffer-end 例外用 lastLine（实际渲染到的最后 buffer 行）判断，
//     而非 startLine+nContent（数学超限 ≠ 真正渲染到末尾）
//
// 鼠标滚动会把 StartLine 推进到 sb 中部，此时起点 sl 仍在覆盖内，
// 但 viewport 尾部（startVY+height）可能超出有效内容末尾 → 需重渲染。
func (s *screenBuffer) coversExtent(sl SLoc, height, bufferLines int) bool {
	if s == nil || s.nContent == 0 {
		return false
	}
	// ★ 起点不能在 sb 之前：鼠标向上回滚时 StartLine 会减小到 sb.startLine 之下，
	//   此时 sb 内容不含 StartLine，必须重渲染。若放行，rowIndexNearest 会错误返回
	//   sb 第一行（row 0=sb.startLine），coversExtent 误判 true → 不补渲染 →
	//   showBuffer 永远 blit sb 第一行内容 → 画面卡住不动。
	if sl.Line < s.startLine.Line {
		return false
	}
	startVY, ok := s.rowIndexOf(sl)
	if !ok {
		// sl 在 sb 范围内但落在装饰行 → nearest 兜底（同区域内向下找内容行，合法）
		startVY, ok = s.rowIndexNearest(sl)
		if !ok {
			return false
		}
	}
	// viewport 屏幕行 [startVY, startVY+height) 必须全在有效内容内
	if startVY+height <= s.nContent {
		return true
	}
	// 尾部超出有效内容：仅当 sb 已渲染到 buffer 最后一行时允许（EOF 空白，无内容可补）
	if s.lastLine >= bufferLines-1 {
		return true
	}
	return false
}

// rowIndexOf 查找 (line, segRow) 二元组对应的 rows 下标。
// 线性扫 rows，O(len(rows))；bufHeight 通常 ≤ 终端高度，开销可忽略。
func (s *screenBuffer) rowIndexOf(sl SLoc) (int, bool) {
	if s == nil {
		return 0, false
	}
	for i, r := range s.rows {
		if r.line == sl.Line && r.segRow == sl.Row {
			return i, true
		}
	}
	return 0, false
}

// lastRowIndexOf 返回 sb 中指定 line 最后一行的索引（= 该 line 的最大 segRow 行）。
// 行内编辑（同 buffer line 变长 wrap 出新视觉行）时，用它在 caseJudge 里估算 cursor
// 在 old sb 中的近似位置：cursor 的 Row 超出了 old sb 记录的范围，但仍在同一 buffer line，
// 属连续编辑小位移，应走 case A 保持起点，而非误判 case C 跳远（详见 relocateVerticalMD）。
func (s *screenBuffer) lastRowIndexOf(line int) (int, bool) {
	if s == nil {
		return 0, false
	}
	idx := -1
	for i, r := range s.rows {
		if r.line == line {
			idx = i // rows 有序，保留最后一个 = 最大 segRow
		}
	}
	if idx < 0 {
		return 0, false
	}
	return idx, true
}

// rowIndexNearest 当 rowIndexOf 落装饰行/边界时，向下找首个内容行。
func (s *screenBuffer) rowIndexNearest(sl SLoc) (int, bool) {
	if s == nil {
		return 0, false
	}
	if idx, ok := s.rowIndexOf(sl); ok {
		return idx, true
	}
	for i, r := range s.rows {
		if r.line >= 0 {
			return i, true
		}
	}
	return 0, false
}

// slocAt 从 rows[vY] 还原 SLoc（line, segRow）。
func (s *screenBuffer) slocAt(vY int) (SLoc, bool) {
	if s == nil || vY < 0 || vY >= len(s.rows) {
		return SLoc{}, false
	}
	r := s.rows[vY]
	return SLoc{Line: r.line, Row: r.segRow}, true
}

// findSegmentContaining 返回包含 line 的 Segment 指针（非拷贝，确保 == 稳定）。
// line < 0 → 返回 nil（prevCursorY=-1 sentinel 边界保护）。
func (w *BufWindow) findSegmentContaining(line int) *md.Segment {
	if line < 0 {
		return nil
	}
	segs := w.Buf.MDSegments
	for i := range segs {
		if segs[i].BufStartLine <= line && line <= segs[i].BufEndLine {
			return &segs[i]
		}
	}
	return nil
}

// renderLimit 返回 renderer 内部循环的上限。
// displayToBuffer 模式下为 2×bufHeight（吸纳跨段展开增量的余量）；
// 其他场景与 bufHeight 一致（保证 native / 旧调用路径行为不变）。
func (w *BufWindow) renderLimit() int {
	if w.sink != nil && w.sb != nil {
		return 2 * w.bufHeight
	}
	return w.bufHeight
}

// setCell 是渲染写屏的 BufWindow 统一入口。
// 转发到当前 sink（realScreenSink 或 screenBuffer）。
func (w *BufWindow) setCell(x, y int, mainc rune, combc []rune, style tcell.Style) {
	if w.sink == nil {
		// 防御性：未初始化的 sink 走真屏（不应发生，但保证不 panic）
		screen.SetContent(x, y, mainc, combc, style)
		return
	}
	w.sink.SetContent(x, y, mainc, combc, style)
}

// setShowCursor 转发到当前 sink 或原生 showCursor。
func (w *BufWindow) setShowCursor(x, y int, c *buffer.Cursor) {
	if _, ok := w.sink.(*screenBuffer); ok {
		if w.sb != nil {
			w.sb.ShowCursor(x, y, c)
		}
	} else {
		w.showCursor(x, y, c.Num == 0)
	}
}

// updatePrevCursor 更新上一帧光标行号，供下一帧 displayToBuffer 的 oldSeg 停止条件用。
// c 是 *buffer.Cursor，其 Y 是 buffer 行号（非屏幕行），与 segment.BufStartLine/BufEndLine 同坐标系。
func (w *BufWindow) updatePrevCursor() {
	if c := w.Buf.GetActiveCursor(); c != nil {
		w.prevCursorY = c.Y
	}
}
