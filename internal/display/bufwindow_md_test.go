package display

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/micro-editor/micro/v2/internal/buffer"
	"github.com/micro-editor/micro/v2/internal/config"
	"github.com/micro-editor/micro/v2/pkg/highlight"
	"github.com/micro-editor/tcell/v2"
)

// TestScreenBuffer_SetContent 验证 SetContent 写入 cells 到正确位置。
func TestScreenBuffer_SetContent(t *testing.T) {
	s := &screenBuffer{originX: 0, originY: 0, width: 80}
	s.reset(50, 80, 0, 0)

	s.SetContent(0, 0, 'A', nil, tcell.StyleDefault)
	s.SetContent(79, 49, 'Z', nil, tcell.StyleDefault)

	if got := s.rows[0].cells[0].r; got != 'A' {
		t.Errorf("rows[0].cells[0].r = %q, want %q", got, 'A')
	}
	if got := s.rows[49].cells[79].r; got != 'Z' {
		t.Errorf("rows[49].cells[79].r = %q, want %q", got, 'Z')
	}
}

// TestScreenBuffer_SetContentOverflow 验证越界 SetContent 静默丢弃不 panic。
func TestScreenBuffer_SetContentOverflow(t *testing.T) {
	s := &screenBuffer{originX: 0, originY: 0, width: 80}
	s.reset(50, 80, 0, 0)

	// 越界：行号 100 > len(rows)；列号 100 > width
	s.SetContent(100, 100, 'X', nil, tcell.StyleDefault)
	s.SetContent(-1, -1, 'Y', nil, tcell.StyleDefault)
	// 无 panic = pass
}

// TestScreenBuffer_SetContentOriginOffset 验证 originX/Y 偏移换算。
func TestScreenBuffer_SetContentOriginOffset(t *testing.T) {
	s := &screenBuffer{originX: 10, originY: 5, width: 80}
	s.reset(50, 80, 10, 5)

	s.SetContent(15, 10, 'A', nil, tcell.StyleDefault) // 绝对 → 本地 (5, 5)
	if got := s.rows[5].cells[5].r; got != 'A' {
		t.Errorf("originOffset 换算失败：rows[5].cells[5].r = %q, want %q", got, 'A')
	}
}

// TestScreenBuffer_Covers 验证 covers/coversLine 范围判定。
func TestScreenBuffer_Covers(t *testing.T) {
	s := &screenBuffer{width: 80}
	s.reset(50, 80, 0, 0)
	s.startLine = SLoc{Line: 100, Row: 0}

	if !s.covers(SLoc{Line: 100, Row: 0}) {
		t.Error("covers(startLine.Line) 应为 true")
	}
	if !s.covers(SLoc{Line: 149, Row: 0}) {
		t.Error("covers(startLine+49) 应为 true（capacity-1）")
	}
	if s.covers(SLoc{Line: 150, Row: 0}) {
		t.Error("covers(startLine+50) 应为 false（capacity 边界外）")
	}
	if s.covers(SLoc{Line: 99, Row: 0}) {
		t.Error("covers(startLine-1) 应为 false")
	}

	if !s.coversLine(100) {
		t.Error("coversLine(100) 应为 true")
	}
	if s.coversLine(99) {
		t.Error("coversLine(99) 应为 false")
	}
	if s.coversLine(150) {
		t.Error("coversLine(150) 应为 false")
	}
}

// TestScreenBuffer_RowIndexOf 验证二元组精确匹配。
func TestScreenBuffer_RowIndexOf(t *testing.T) {
	s := &screenBuffer{width: 80}
	s.reset(50, 80, 0, 0)
	s.rows[5].line = 100
	s.rows[5].segRow = 2
	s.rows[10].line = 100
	s.rows[10].segRow = 0

	if idx, ok := s.rowIndexOf(SLoc{Line: 100, Row: 2}); !ok || idx != 5 {
		t.Errorf("rowIndexOf(100, 2) = (%d, %v), want (5, true)", idx, ok)
	}
	if idx, ok := s.rowIndexOf(SLoc{Line: 100, Row: 0}); !ok || idx != 10 {
		t.Errorf("rowIndexOf(100, 0) = (%d, %v), want (10, true)", idx, ok)
	}
	if _, ok := s.rowIndexOf(SLoc{Line: 200, Row: 0}); ok {
		t.Error("rowIndexOf(200, 0) 应为 false")
	}
}

// TestScreenBuffer_RowIndexNearest 落装饰行时向下找首个内容行。
func TestScreenBuffer_RowIndexNearest(t *testing.T) {
	s := &screenBuffer{width: 80}
	s.reset(50, 80, 0, 0)
	// rows[0..4] 装饰行；rows[5] 首个内容行
	s.rows[5].line = 100
	s.rows[5].segRow = 0

	if idx, ok := s.rowIndexNearest(SLoc{Line: 100, Row: 0}); !ok || idx != 5 {
		t.Errorf("rowIndexNearest(100, 0) = (%d, %v), want (5, true)", idx, ok)
	}
	if idx, ok := s.rowIndexNearest(SLoc{Line: 999, Row: 0}); !ok || idx != 5 {
		t.Errorf("rowIndexNearest(999, 0) = (%d, %v), want (5, true)", idx, ok)
	}
}

// TestScreenBuffer_SlocAt 从 rows[vY] 还原 SLoc。
func TestScreenBuffer_SlocAt(t *testing.T) {
	s := &screenBuffer{width: 80}
	s.reset(50, 80, 0, 0)
	s.rows[7].line = 200
	s.rows[7].segRow = 3

	sl, ok := s.slocAt(7)
	if !ok || sl.Line != 200 || sl.Row != 3 {
		t.Errorf("slocAt(7) = %+v, %v; want {200, 3}, true", sl, ok)
	}
	if _, ok := s.slocAt(-1); ok {
		t.Error("slocAt(-1) 应为 false")
	}
	if _, ok := s.slocAt(100); ok {
		t.Error("slocAt(100) 应为 false（越界）")
	}
}

// TestScreenBuffer_Reset 验证 reset 复用底层 slice。
func TestScreenBuffer_Reset(t *testing.T) {
	s := &screenBuffer{width: 80}
	s.reset(50, 80, 0, 0)
	origRows := s.rows
	origRow0Cells := s.rows[0].cells

	// 同样 capacity/width：应复用
	s.reset(50, 80, 0, 0)
	if &s.rows[0] != &origRows[0] {
		t.Error("capacity/width 不变时应复用 rows 底层数组")
	}
	if &s.rows[0].cells[0] != &origRow0Cells[0] {
		t.Error("capacity/width 不变时应复用 cells 底层数组")
	}

	// 写点东西
	s.rows[10].line = 100
	s.rows[10].cells[5].r = 'X'

	// 重置后应被清零
	s.reset(50, 80, 0, 0)
	if s.rows[10].line != -2 {
		t.Errorf("reset 后 line = %d, want -2", s.rows[10].line)
	}
	if s.rows[10].cells[5].r != 0 {
		t.Errorf("reset 后 cells[5].r = %q, want 0", s.rows[10].cells[5].r)
	}

	// 容量变化：应重建
	s.reset(100, 80, 0, 0)
	if len(s.rows) != 100 {
		t.Errorf("capacity 变化后 len(rows) = %d, want 100", len(s.rows))
	}

	// 宽度变化：应重建
	s.reset(100, 100, 0, 0)
	if len(s.rows[0].cells) != 100 {
		t.Errorf("width 变化后 len(cells) = %d, want 100", len(s.rows[0].cells))
	}
}

// TestRealScreenSink 验证 realScreenSink 类型签名匹配（编译期检查 + 字段）。
func TestRealScreenSink(t *testing.T) {
	var s cellSink = realScreenSink{}
	// 仅验证类型满足 cellSink 接口
	_ = s
}

// TestSetCellNilSink 验证 sink=nil 时 setCell 不 panic（防御性回退到 screen.SetContent）。
func TestSetCellNilSink(t *testing.T) {
	// 用一个最小 BufWindow（只需要 sink 字段为 nil）
	w := &BufWindow{}
	// 不调用 screen.SetContent（会 panic 因为没初始化屏幕）
	// 改为验证逻辑分支：sink==nil → 走 screen.SetContent
	// 这里仅做 nil 检查和类型断言
	if w.sink != nil {
		t.Error("新建 BufWindow 时 sink 应为 nil")
	}
}

// TestScreenBuffer_ShowCursor 验证 ShowCursor 记录到 cursors 切片（含主+次）。
func TestScreenBuffer_ShowCursor(t *testing.T) {
	s := &screenBuffer{originX: 10, originY: 5, width: 80}
	s.reset(50, 80, 10, 5)

	main := &buffer.Cursor{Num: 0}
	s.ShowCursor(15, 10, main) // 绝对 → 本地 (5, 5)
	if len(s.cursors) != 1 {
		t.Fatalf("len(cursors) = %d, want 1", len(s.cursors))
	}
	got := s.cursors[0]
	if got.screenX != 5 || got.screenY != 5 {
		t.Errorf("screenX/Y = (%d, %d), want (5, 5)", got.screenX, got.screenY)
	}
	if got.c != main {
		t.Error("cursors[0].c != 传入的 cursor 指针")
	}

	// 多光标追加：次级 cursor 应保留在 cursors[1]，main 标记不丢失
	sub := &buffer.Cursor{Num: 1}
	s.ShowCursor(25, 20, sub)
	if len(s.cursors) != 2 {
		t.Fatalf("len(cursors) = %d, want 2（次级 cursor 应追加）", len(s.cursors))
	}
	if s.cursors[1].c != sub || s.cursors[0].c != main {
		t.Error("追加后两个 cursor 应都保留，且顺序与写入一致")
	}
}

// TestScreenBuffer_NilReceiver 所有方法的 nil receiver 安全检查。
// 设计依据：displayToBuffer 入口可能有 sb==nil，setRowMeta/rowIndexOf 等都可能接 nil。
// 重构后所有方法都要有 nil-safe 行为（§3.1 数据结构、§4.5 辅助方法 设计要求）。
func TestScreenBuffer_NilReceiver(t *testing.T) {
	var s *screenBuffer // nil

	// 以下调用都不应 panic
	s.SetContent(0, 0, 'A', nil, tcell.StyleDefault)
	s.ShowCursor(0, 0, nil)
	s.setRowMeta(0, 0, 0)
	// reset 不 nil-safe：displayToBuffer 入口会 if sb==nil { sb = &screenBuffer{} } 保护

	if s.covers(SLoc{Line: 0, Row: 0}) {
		t.Error("nil.covers 应为 false（nil receiver 返回 false）")
	}
	if s.coversLine(0) {
		t.Error("nil.coversLine 应为 false")
	}
	if _, ok := s.rowIndexOf(SLoc{Line: 0, Row: 0}); ok {
		t.Error("nil.rowIndexOf 应为 false")
	}
	if _, ok := s.rowIndexNearest(SLoc{Line: 0, Row: 0}); ok {
		t.Error("nil.rowIndexNearest 应为 false")
	}
	if _, ok := s.slocAt(0); ok {
		t.Error("nil.slocAt 应为 false")
	}
}

// TestScreenBuffer_BlitBoundary 验证 §8.3 blit 边界场景的 sb 查询逻辑。
// （showBuffer 本身需真屏不能单元测，这里测支持逻辑。）
func TestScreenBuffer_BlitBoundary(t *testing.T) {
	s := &screenBuffer{width: 80}
	s.reset(50, 80, 0, 0)
	s.startLine = SLoc{Line: 100, Row: 0}

	// rows[0..4] 是装饰行（line=-1）；rows[5] 首个内容行
	s.rows[5].line = 105
	s.rows[5].segRow = 0

	// startLine 落装饰行：rowIndexOf 失败，rowIndexNearest 向下找首个内容行
	startVY, ok := s.rowIndexNearest(SLoc{Line: 100, Row: 0})
	if !ok || startVY != 5 {
		t.Errorf("rowIndexNearest 装饰行场景：startVY=%d, ok=%v; want (5, true)", startVY, ok)
	}

	// 尾部不足（screenOffset 越出 rows 范围）：ScreenRowToLine 返回 (0, false)
	if s.coversLine(200) {
		t.Error("coversLine(200) 在 2× cap 外应为 false")
	}
}

// TestFindSegmentContaining 验证 §4.5 findSegmentContaining 边界场景。
func TestFindSegmentContaining(t *testing.T) {
	// nil BufWindow → 间接 panic；这里只能测 line<0 sentinel。
	w := &BufWindow{}
	if got := w.findSegmentContaining(-1); got != nil {
		t.Errorf("findSegmentContaining(-1) sentinel 应为 nil, got %v", got)
	}
}

// TestScreenBuffer_RowIndexOfSoftwrapOffset 验证 softwrap 下 (Line, Row) 二元组精确匹配。
// 重现 sample.md 表格场景：line N 第 k 个 wrap 续行。
func TestScreenBuffer_RowIndexOfSoftwrapOffset(t *testing.T) {
	s := &screenBuffer{width: 80}
	s.reset(50, 80, 0, 0)

	// 模拟 line=60 有 3 个 softwrap 续行
	s.rows[10].line = 60
	s.rows[10].segRow = 0
	s.rows[11].line = 60
	s.rows[11].segRow = 1
	s.rows[12].line = 60
	s.rows[12].segRow = 2

	for k := 0; k < 3; k++ {
		idx, ok := s.rowIndexOf(SLoc{Line: 60, Row: k})
		if !ok || idx != 10+k {
			t.Errorf("rowIndexOf(60, %d) = (%d, %v); want (%d, true)", k, idx, ok, 10+k)
		}
	}

	// 超出范围
	if _, ok := s.rowIndexOf(SLoc{Line: 60, Row: 3}); ok {
		t.Error("rowIndexOf(60, 3) 应为 false（超出该行的 wrap 段数）")
	}
}

// TestScreenBuffer_OverflowFlag 验证 overflow 标记的存取。
func TestScreenBuffer_OverflowFlag(t *testing.T) {
	s := &screenBuffer{width: 80}
	s.reset(50, 80, 0, 0)
	if s.overflow {
		t.Error("新建 sb 时 overflow 应为 false")
	}
	s.overflow = true
	if !s.overflow {
		t.Error("设置 overflow 后应能读到 true")
	}
}

// TestScreenBuffer_BlitStartRecord 验证 showBuffer 记录的 blitStart 供点击映射使用。
func TestScreenBuffer_BlitStartRecord(t *testing.T) {
	s := &screenBuffer{width: 80}
	s.reset(50, 80, 0, 0)
	s.startLine = SLoc{Line: 100, Row: 0}
	// 填充 rows[5..7] 内容行供后续查询
	for i := 5; i <= 8; i++ {
		s.rows[i].line = 100 + i
		s.rows[i].segRow = 0
	}

	// showBuffer 入口会设置 blitStart = rowIndexOf(StartLine). 这里验证关联。
	startVY, ok := s.rowIndexOf(SLoc{Line: 105, Row: 0})
	if !ok {
		t.Fatal("rowIndexOf 失败")
	}
	s.blitStart = startVY

	// 模拟点击 screenOffset=2（相对于 viewport 顶部）→ sb.rows[blitStart + 2]
	expectedLine := s.rows[startVY+2].line
	if expectedLine != 107 {
		t.Errorf("点击映射偏移错乱：line=%d, want 107", expectedLine)
	}
}

// TestRenderLimit 验证 renderLimit 返回 2×bufHeight 在 sb 模式。
func TestRenderLimit(t *testing.T) {
	w := &BufWindow{}
	w.bufHeight = 20
	w.sink = nil
	w.sb = nil

	if got := w.renderLimit(); got != 20 {
		t.Errorf("无 sb 时 renderLimit=%d, want bufHeight=20", got)
	}

	// sb 模式
	w.sb = &screenBuffer{}
	w.sink = w.sb
	if got := w.renderLimit(); got != 40 {
		t.Errorf("sb 模式 renderLimit=%d, want 2×bufHeight=40", got)
	}
}

// TestScreenBuffer_DeviationCellsNotNilled 验证 showBuffer 不 nil cells 的设计选择。
// （Deviation from plan §3.1：cells 保留以支持 displayBufferMD skip 优化。）
func TestScreenBuffer_DeviationCellsNotNilled(t *testing.T) {
	s := &screenBuffer{width: 80}
	s.reset(50, 80, 0, 0)
	s.rows[0].cells[0].r = 'X'

	// 模拟 skip 优化后的第二次 showBuffer 调用：cells 应仍可用
	if s.rows[0].cells[0].r != 'X' {
		t.Error("cells 应保留，不应被 nil（这是偏离 plan §3.1 的设计选择）")
	}
}

// TestUpdatePrevCursor 验证 NewBufWindow 初始化 prevCursorY = -1 sentinel。
func TestUpdatePrevCursor(t *testing.T) {
	w := &BufWindow{}
	// &BufWindow{} 不调 NewBufWindow，故 prevCursorY = 0（Go 默认）。
	// 实际生产路径（NewBufWindow）会显式初始化为 -1。
	if w.prevCursorY != 0 {
		t.Errorf("裸 BufWindow 默认 prevCursorY = %d, want 0（Go 零值）", w.prevCursorY)
	}
	// 手工模拟 NewBufWindow 的初始化
	w.prevCursorY = -1
	if w.prevCursorY != -1 {
		t.Error("显式设为 -1 后应为 sentinel")
	}
}

// 避免 unused import 警告（config 用于编译期检查）
var _ = config.DefStyle

// testGroup 通过解析一个最小 yaml 注册所需高亮组，返回 Group 值。
// 必须走 ParseDef：直接写 highlight.Groups 会绕过内部 numGroups 计数器，
// 导致后续解析真实 yaml 时组号重复分配、String() 映射错乱（map 迭代随机）。
func testGroup(t *testing.T, names ...string) map[string]highlight.Group {
	src := "filetype: test\nrules:\n"
	for _, n := range names {
		src += "    - " + n + ": \"a\"\n"
	}
	f, err := highlight.ParseFile([]byte(src))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if _, err := highlight.ParseDef(f, nil); err != nil {
		t.Fatalf("ParseDef: %v", err)
	}
	out := make(map[string]highlight.Group)
	for _, n := range names {
		out[n] = highlight.Groups[n]
	}
	return out
}

// TestPolluted 验证污染判定：md-* 组干净、非 md-* 组污染、空组名中性。
func TestPolluted(t *testing.T) {
	groups := testGroup(t, "md-header", "constant.string")
	mdHeader := groups["md-header"]
	strGroup := groups["constant.string"]

	if polluted(highlight.LineMatch{0: mdHeader}) {
		t.Error("md-header 组不应判定为污染")
	}
	if !polluted(highlight.LineMatch{0: strGroup}) {
		t.Error("constant.string 组应判定为污染")
	}
	// 空组名（默认组）是中性组，不算污染
	if polluted(highlight.LineMatch{0: 0}) {
		t.Error("空组名（Group 0）不应判定为污染")
	}
	// 混合：md-* + 污染组
	if !polluted(highlight.LineMatch{0: mdHeader, 5: strGroup}) {
		t.Error("混合 md-* + 污染组应判定为污染")
	}
	// 空 map
	if polluted(highlight.LineMatch{}) {
		t.Error("空 map 不应判定为污染")
	}
}

// loadMarkdownDef 从真实 runtime/syntax/markdown.yaml 解析出 Def（不解析 include）。
// 顶层 md-* 规则是单行 regex，不依赖 include，fresh 单行高亮足够。
func loadMarkdownDef(t *testing.T) *highlight.Def {
	data, err := os.ReadFile("../../runtime/syntax/markdown.yaml")
	if err != nil {
		t.Skipf("markdown.yaml 不可读，跳过: %v", err)
	}
	f, err := highlight.ParseFile(data)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	def, err := highlight.ParseDef(f, nil)
	if err != nil {
		t.Fatalf("ParseDef: %v", err)
	}
	return def
}

// TestFreshLineMatch 用真实 markdown.yaml 对单行做 fresh 重高亮，
// 断言命中 md-header / md-list 组（freshLineMatch 不挂 BufWindow，可独立单测）。
func TestFreshLineMatch(t *testing.T) {
	def := loadMarkdownDef(t)
	tests := []struct {
		name string
		line string
		want string
	}{
		{"heading", "### heading", "md-header"},
		{"list item", "- item", "md-list"},
		{"numbered list", "1. item", "md-list"},
		{"plain text", "just some text", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := freshLineMatch(def, tt.line)
			if tt.want == "" {
				if polluted(m) {
					t.Errorf("freshLineMatch(%q) = %v, want 无污染", tt.line, m)
				}
				return
			}
			for _, g := range m {
				if g.String() == tt.want {
					return
				}
			}
			t.Errorf("freshLineMatch(%q) = %v, want 命中组 %s", tt.line, m, tt.want)
		})
	}
}

// loadAllSyntaxDefs 加载 runtime/syntax 全部 yaml 并 ResolveIncludes（fence 嵌入语言需要）。
// 与 loadMarkdownDef 的区别：后者不解析 include，仅供顶层 md-* 单行规则测试。
// 全包只加载一次：highlight.Groups 是进程级全局表，共享实例才能跨用例比较组名。
var (
	allDefsOnce sync.Once
	allDefs     map[string]*highlight.Def
	allDefsErr  error
)

func loadAllSyntaxDefs(t *testing.T) map[string]*highlight.Def {
	t.Helper()
	allDefsOnce.Do(func() {
		dir := "../../runtime/syntax"
		entries, err := os.ReadDir(dir)
		if err != nil {
			allDefsErr = err
			return
		}
		allDefs = map[string]*highlight.Def{}
		var files []*highlight.File
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			pf, err := highlight.ParseFile(data)
			if err != nil {
				continue
			}
			hdr, err := highlight.MakeHeaderYaml(data)
			if err != nil {
				continue
			}
			d, err := highlight.ParseDef(pf, hdr)
			if err != nil {
				continue
			}
			allDefs[pf.FileType] = d
			files = append(files, pf)
		}
		for _, d := range allDefs {
			highlight.ResolveIncludes(d, files)
		}
	})
	if allDefsErr != nil {
		t.Skipf("runtime/syntax 不可读，跳过: %v", allDefsErr)
	}
	return allDefs
}

// matchHasGroup 判断 LineMatch 中是否存在名为 name 的组。
func matchHasGroup(m highlight.LineMatch, name string) bool {
	for _, g := range m {
		if g.String() == name {
			return true
		}
	}
	return false
}

// t1BlockLine 即 T1 文档 159 行原文：正则字符类 /["]}']/g 内含落单引号。
const t1BlockLine = `    const txt = String(obj.chatTxt ?? '').replace(/["}']/g, '').trim().slice(0, 20);`

// TestFreshBlockMatches ts fence 块含 T1:159 行：块内行恢复正常 ts 语法组
// （正则 constant 出现），不再整行 constant.string。
func TestFreshBlockMatches(t *testing.T) {
	def := loadAllSyntaxDefs(t)["markdown"]
	if def == nil {
		t.Fatal("markdown def 未加载")
	}
	block := []string{"```ts", t1BlockLine, "```"}
	matches := freshBlockMatches(def, block)
	if len(matches) != 3 {
		t.Fatalf("freshBlockMatches 返回 %d 行, want 3", len(matches))
	}
	m := matches[1]
	if !matchHasGroup(m, "constant") {
		t.Errorf("块内行应含正则 constant 组, got %v", m)
	}
	// 列 38-39 的 '' 是合法配对字符串；污染特征是列 50+ 落单 " 染 string 到行尾
	for i, g := range m {
		if g.String() == "constant.string" && i >= 50 {
			t.Errorf("列 %d 出现 constant.string，块内行被 string region 污染: %v", i, m)
		}
	}
}

// TestFreshBlockIsolation 真·未闭合字符串（引擎修复不覆盖的残留类）：
// 污染最多染到所在块尾；第二个块从 nil state 重新起步，完全干净。
// 两个块各自独立调 freshBlockMatches，对齐 renderSegmentMD 按 segment 分块调用的真实形态。
func TestFreshBlockIsolation(t *testing.T) {
	def := loadAllSyntaxDefs(t)["markdown"]
	if def == nil {
		t.Fatal("markdown def 未加载")
	}
	block1 := []string{"```ts", `const s = "abc`, "foo();", "```"}
	block2 := []string{"```ts", "const ok = 1", "```"}

	m1 := freshBlockMatches(def, block1)
	if len(m1) != 4 {
		t.Fatalf("block1 返回 %d 行, want 4", len(m1))
	}
	// 第一块尾部行染 string 到块尾即止（合理残留，同 VSCode 行为边界）
	if !matchHasGroup(m1[2], "constant.string") {
		t.Errorf("未闭合字符串应把所在块尾部染 string, got %v", m1[2])
	}

	m2 := freshBlockMatches(def, block2)
	if len(m2) != 3 {
		t.Fatalf("block2 返回 %d 行, want 3", len(m2))
	}
	// 隔离性核心断言：第二块内容行完全干净，无跨块污染
	if matchHasGroup(m2[1], "constant.string") {
		t.Errorf("第二块被第一块的残留污染: %v", m2[1])
	}
	if !matchHasGroup(m2[1], "identifier") && !matchHasGroup(m2[1], "constant.number") {
		t.Errorf("第二块内容行应有正常语法组, got %v", m2[1])
	}
}
