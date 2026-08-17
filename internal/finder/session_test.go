package finder

import (
	"os"
	"sync"
	"testing"

	"github.com/micro-editor/micro/v2/internal/config"
	"github.com/micro-editor/micro/v2/internal/screen"
	"github.com/micro-editor/tcell/v2"
)

// ---- listRowAt 测试 ----

func TestListRowAt_Breadcrumb(t *testing.T) {
	l := &fileList{rect: Rect{X: 5, Y: 11, W: 39, H: 20}, showEntries: []*entry{{name: "a"}, {name: "b"}}, listH: 18, topIdx: 0}

	cursor, ok := l.listRowAt(11) // rect.Y = breadcrumb
	if !ok || cursor != 0 {
		t.Errorf("breadcrumb: got (%d, %v), want (0, true)", cursor, ok)
	}
}

func TestListRowAt_FirstEntry(t *testing.T) {
	l := &fileList{rect: Rect{X: 5, Y: 11, W: 39, H: 20}, showEntries: []*entry{{name: "a"}, {name: "b"}}, listH: 18, topIdx: 0}

	cursor, ok := l.listRowAt(12) // rect.Y+1
	if !ok || cursor != 1 {
		t.Errorf("first entry: got (%d, %v), want (1, true)", cursor, ok)
	}
}

func TestListRowAt_KthEntry(t *testing.T) {
	l := &fileList{rect: Rect{X: 5, Y: 11, W: 39, H: 20}, showEntries: []*entry{{name: "a"}, {name: "b"}, {name: "c"}, {name: "d"}}, listH: 18, topIdx: 2}

	for k, want := range []int{3, 4} {
		cursor, ok := l.listRowAt(12 + k)
		if !ok || cursor != want {
			t.Errorf("k=%d: got (%d, %v), want (%d, true)", k, cursor, ok, want)
		}
	}
}

func TestListRowAt_Scrolled(t *testing.T) {
	l := &fileList{rect: Rect{X: 5, Y: 11, W: 39, H: 20}, showEntries: []*entry{{name: "a"}, {name: "b"}, {name: "c"}}, listH: 2, topIdx: 1}

	cursor, ok := l.listRowAt(12)
	if !ok || cursor != 2 {
		t.Errorf("scrolled first entry: got (%d, %v), want (2, true)", cursor, ok)
	}
}

func TestListRowAt_BlankRow(t *testing.T) {
	l := &fileList{rect: Rect{X: 5, Y: 11, W: 39, H: 20}, showEntries: []*entry{{name: "a"}}, listH: 18, topIdx: 0}

	_, ok := l.listRowAt(13) // rect.Y+2：条目只有 1 个，后面空白
	if ok {
		t.Errorf("blank row: got ok=true, want ok=false")
	}
}

func TestListRowAt_Outside(t *testing.T) {
	l := &fileList{rect: Rect{X: 5, Y: 11, W: 39, H: 20}, showEntries: []*entry{{name: "a"}}, listH: 5, topIdx: 0}

	cases := []struct {
		name string
		my   int
	}{
		{"top border", 10},              // above rect.Y
		{"hint row", 11 + 5 + 1},        // rect.Y + listH + 1
		{"bottom border", 11 + 20 - 1},  // rect.Y + H - 1
		{"way below", 100},
	}
	for _, c := range cases {
		_, ok := l.listRowAt(c.my)
		if ok {
			t.Errorf("%s: got ok=true, want ok=false", c.name)
		}
	}
}

func TestListRowAt_MoreEntriesThanView(t *testing.T) {
	l := &fileList{rect: Rect{X: 5, Y: 11, W: 39, H: 20}, showEntries: []*entry{{name: "a"}, {name: "b"}, {name: "c"}, {name: "d"}, {name: "e"}}, listH: 3, topIdx: 0}

	for i := 0; i < 3; i++ {
		_, ok := l.listRowAt(12 + i)
		if !ok {
			t.Errorf("visible row %d: got ok=false, want ok=true", i)
		}
	}
	_, ok := l.listRowAt(12 + 3)
	if ok {
		t.Errorf("beyond visible: got ok=true, want ok=false")
	}
}

// ---- hitTest 测试 ----

type testRegion struct {
	r Rect
}

func (tr testRegion) Rect() Rect            { return tr.r }
func (tr testRegion) Display()              {}
func (tr testRegion) HandleMouse(_ *tcell.EventMouse) bool { return false }

func TestHitTest_Hit(t *testing.T) {
	regions := []testRegion{
		{r: Rect{X: 5, Y: 10, W: 10, H: 5}},
		{r: Rect{X: 20, Y: 10, W: 10, H: 5}},
	}
	ev := tcell.NewEventMouse(7, 12, tcell.Button1, 0, "")
	idx := hitTest(regions, ev)
	if idx != 0 {
		t.Errorf("hit first region: got %d, want 0", idx)
	}
}

func TestHitTest_HitSecond(t *testing.T) {
	regions := []testRegion{
		{r: Rect{X: 5, Y: 10, W: 10, H: 5}},
		{r: Rect{X: 20, Y: 10, W: 10, H: 5}},
	}
	ev := tcell.NewEventMouse(22, 12, tcell.Button1, 0, "")
	idx := hitTest(regions, ev)
	if idx != 1 {
		t.Errorf("hit second region: got %d, want 1", idx)
	}
}

func TestHitTest_Miss(t *testing.T) {
	regions := []testRegion{
		{r: Rect{X: 5, Y: 10, W: 10, H: 5}},
	}
	ev := tcell.NewEventMouse(0, 0, tcell.Button1, 0, "")
	idx := hitTest(regions, ev)
	if idx != -1 {
		t.Errorf("miss: got %d, want -1", idx)
	}
}

func TestHitTest_GapBetweenRegions(t *testing.T) {
	regions := []testRegion{
		{r: Rect{X: 5, Y: 10, W: 10, H: 5}},
		{r: Rect{X: 20, Y: 10, W: 10, H: 5}},
	}
	// 点 gap 列 (15..19) 应返回 -1
	ev := tcell.NewEventMouse(16, 12, tcell.Button1, 0, "")
	idx := hitTest(regions, ev)
	if idx != -1 {
		t.Errorf("gap: got %d, want -1", idx)
	}
}

func TestHitTest_EmptySlice(t *testing.T) {
	regions := []testRegion{}
	ev := tcell.NewEventMouse(5, 5, tcell.Button1, 0, "")
	idx := hitTest(regions, ev)
	if idx != -1 {
		t.Errorf("empty slice: got %d, want -1", idx)
	}
}

func TestHitTest_RightEdgeExclusive(t *testing.T) {
	regions := []testRegion{
		{r: Rect{X: 5, Y: 10, W: 10, H: 5}},
	}
	// X+W = 15，点 15 应返回 -1（右边界 exclusive）
	ev := tcell.NewEventMouse(15, 12, tcell.Button1, 0, "")
	idx := hitTest(regions, ev)
	if idx != -1 {
		t.Errorf("right edge: got %d, want -1", idx)
	}
	// X+W-1 = 14 应命中
	ev2 := tcell.NewEventMouse(14, 12, tcell.Button1, 0, "")
	idx2 := hitTest(regions, ev2)
	if idx2 != 0 {
		t.Errorf("rightmost col: got %d, want 0", idx2)
	}
}

func TestHitTest_BottomEdgeExclusive(t *testing.T) {
	regions := []testRegion{
		{r: Rect{X: 5, Y: 10, W: 10, H: 5}},
	}
	// Y+H = 15，点 15 应返回 -1
	ev := tcell.NewEventMouse(7, 15, tcell.Button1, 0, "")
	idx := hitTest(regions, ev)
	if idx != -1 {
		t.Errorf("bottom edge: got %d, want -1", idx)
	}
}
// ---- Step 2 集成测试 ----

var screenOnce sync.Once

// ensureScreen 初始化 SimScreen 与默认全局设置，供 Display / FFM 等需要屏幕的 Step 2 集成测试用。
// 只跑一次；多次调用安全。SimScreen 全局共享，但每个 test 用独立 t.TempDir() 隔离数据。
func ensureScreen(t *testing.T) {
	t.Helper()
	screenOnce.Do(func() {
		// config 必须在屏幕之前初始化（Screen 检查 mouse 选项）
		if config.GlobalSettings == nil {
			config.GlobalSettings = config.DefaultAllSettings()
		}
		if err := os.Setenv("TERM", "xterm-256color"); err != nil {
			t.Fatal(err)
		}
		if _, err := screen.InitSimScreen(); err != nil {
			t.Fatalf("InitSimScreen: %v", err)
		}
		screen.Events = make(chan tcell.Event, 8)
	})
}

// openTestSession 构造一个真实 Session 并 Open；dirs 注入到 history，cwd 是 fileList 起始目录。
// 同一测试结束后由 t.Cleanup 恢复 config.ConfigDir。
func openTestSession(t *testing.T, w, h int, dirs []string) *Session {
	t.Helper()
	ensureScreen(t)
	dir := t.TempDir()
	saved := config.ConfigDir
	config.ConfigDir = dir
	t.Cleanup(func() { config.ConfigDir = saved })

	if len(dirs) > 0 {
		normalized := make([]string, 0, len(dirs))
		for _, d := range dirs {
			if len(d) == 0 || d[len(d)-1] != os.PathSeparator {
				d += string(os.PathSeparator)
			}
			normalized = append(normalized, d)
		}
		writeDirHistory(normalized)
	}

	cwd := t.TempDir()
	fm := NewSession()
	if !fm.Open(Rect{X: 0, Y: 0, W: w, H: h}, cwd, "", false, nil) {
		t.Fatalf("Open(W=%d,H=%d) failed", w, h)
	}
	return fm
}

// ---- 高度门槛 ----

func TestSession_Height19_NoHistoryEvenWithDirs(t *testing.T) {
	fm := openTestSession(t, 60, 19, []string{"/a", "/b", "/c"})
	if fm.history != nil {
		t.Errorf("H=19: history should not be constructed; got %+v", fm.history)
	}
	if len(fm.regions) != 2 {
		t.Errorf("H=19: regions len=%d, want 2 (fileList+preview)", len(fm.regions))
	}
	if len(fm.keyboardRegions) != 1 {
		t.Errorf("H=19: keyboardRegions len=%d, want 1 (only fileList)", len(fm.keyboardRegions))
	}
	if fm.historyY != 0 {
		t.Errorf("H=19: historyY=%d, want 0", fm.historyY)
	}
}

func TestSession_Height20_NoHistoryWithEmptyDirs(t *testing.T) {
	fm := openTestSession(t, 60, 20, nil) // 不写入 history.json
	if fm.history != nil {
		t.Errorf("H=15 empty dirs: history should not be constructed")
	}
	if fm.historyY != 0 {
		t.Errorf("H=15 empty dirs: historyY=%d, want 0", fm.historyY)
	}
}

func TestSession_Height20_WithDirs_GeometryExact(t *testing.T) {
	// W=60，fileselectwidth=0.4，pickerW = max(20, 24) = 24
	// fm.rect = (0, 0, 60, 19)；previewX = 23；fullListRect = (0, 1, 23, 17)
	// historyBlockH = 6，listRect.H = 11，historyRect = (0, 13, 23, 5)，historyY = 12
	fm := openTestSession(t, 60, 20, []string{"/path/a", "/path/b", "/path/c", "/path/d"})
	if fm.history == nil {
		t.Fatalf("H=20 with dirs: history should be constructed")
	}
	if fm.historyY != 12 { // = historyRect.Y - 1 = 13 - 1 = 12
		t.Errorf("historyY: got %d, want 12", fm.historyY)
	}
	if got, want := fm.list.rect.H, 11; got != want {
		t.Errorf("fileList.rect.H: got %d, want %d", got, want)
	}
	if got, want := fm.list.listH, 9; got != want {
		t.Errorf("fileList.listH: got %d, want %d", got, want)
	}
	if got, want := fm.history.rect, (Rect{X: 0, Y: 13, W: 23, H: 5}); got != want {
		t.Errorf("history.rect: got %v, want %v", got, want)
	}
}

// ---- slice 顺序 ----

func TestSession_Height20_SliceOrder(t *testing.T) {
	fm := openTestSession(t, 60, 20, []string{"/a"})
	if got, want := len(fm.regions), 3; got != want {
		t.Errorf("regions len: got %d, want %d (fileList+history+preview)", got, want)
	}
	if got, want := len(fm.keyboardRegions), 2; got != want {
		t.Errorf("keyboardRegions len: got %d, want %d (fileList+history)", got, want)
	}
	if _, ok := fm.regions[0].(*fileList); !ok {
		t.Errorf("regions[0]: got %T, want *fileList", fm.regions[0])
	}
	if _, ok := fm.regions[1].(*historyList); !ok {
		t.Errorf("regions[1]: got %T, want *historyList", fm.regions[1])
	}
	if _, ok := fm.regions[2].(*preview); !ok {
		t.Errorf("regions[2]: got %T, want *preview", fm.regions[2])
	}
	if _, ok := fm.keyboardRegions[0].(*fileList); !ok {
		t.Errorf("keyboardRegions[0]: got %T, want *fileList", fm.keyboardRegions[0])
	}
	if _, ok := fm.keyboardRegions[1].(*historyList); !ok {
		t.Errorf("keyboardRegions[1]: got %T, want *historyList", fm.keyboardRegions[1])
	}
}

func TestSession_Height19_SliceOrderNoHistory(t *testing.T) {
	fm := openTestSession(t, 60, 19, []string{"/a"})
	if got, want := len(fm.regions), 2; got != want {
		t.Errorf("H=19: regions len=%d, want %d", got, want)
	}
	if got, want := len(fm.keyboardRegions), 1; got != want {
		t.Errorf("H=19: keyboardRegions len=%d, want %d", got, want)
	}
}

// ---- 初始 focus ----

func TestSession_InitialFocusOnFileList(t *testing.T) {
	fm := openTestSession(t, 60, 20, []string{"/a"})
	if fm.focus != 0 {
		t.Errorf("focus: got %d, want 0 (fileList)", fm.focus)
	}
	if !fm.list.focused {
		t.Errorf("fileList.focused: got false, want true")
	}
	if fm.history.focused {
		t.Errorf("history.focused: got true, want false")
	}
}

// ---- Tab 轮转 ----

func TestSession_TabCyclesBetweenFileListAndHistory(t *testing.T) {
	fm := openTestSession(t, 60, 20, []string{"/a"})

	fm.HandleEvent(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone, ""))
	if fm.focus != 1 {
		t.Errorf("after Tab: focus=%d, want 1", fm.focus)
	}
	if !fm.history.focused {
		t.Errorf("history.focused after Tab: got false, want true")
	}
	if fm.list.focused {
		t.Errorf("fileList.focused after Tab: got true, want false")
	}

	fm.HandleEvent(tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModNone, ""))
	if fm.focus != 0 {
		t.Errorf("after Backtab: focus=%d, want 0", fm.focus)
	}
	if !fm.list.focused {
		t.Errorf("fileList.focused after Backtab: got false, want true")
	}
}

func TestSession_TabNoHistoryNoOp(t *testing.T) {
	fm := openTestSession(t, 60, 14, []string{"/a"})
	fm.HandleEvent(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone, ""))
	if fm.focus != 0 {
		t.Errorf("Tab with no history: focus=%d, want 0", fm.focus)
	}
}

// ---- FFM：鼠标 move / click ----

func TestSession_MouseClickInHistorySwitchesFocus(t *testing.T) {
	fm := openTestSession(t, 60, 20, []string{"/path/a", "/path/b"})
	// 点 history 内容区 (col 5, row 14) — 应命中 keyboardRegions[1] (history)
	ev := tcell.NewEventMouse(5, 14, tcell.Button1, 0, "")
	fm.HandleEvent(ev)
	if fm.focus != 1 {
		t.Errorf("after click in history: focus=%d, want 1", fm.focus)
	}
	if !fm.history.focused {
		t.Errorf("history.focused after click: got false")
	}
}

func TestSession_MouseClickInPreviewDoesNotChangeFocus(t *testing.T) {
	fm := openTestSession(t, 60, 15, []string{"/a"})
	// preview 区从 previewX+1 开始 (= 24)。点 preview 中部 (col 30, row 5)
	ev := tcell.NewEventMouse(30, 5, tcell.Button1, 0, "")
	fm.HandleEvent(ev)
	if fm.focus != 0 {
		t.Errorf("preview click changed focus: got %d, want 0", fm.focus)
	}
}

func TestSession_MouseMoveInHistoryDoesNotSwitchFocus(t *testing.T) {
	fm := openTestSession(t, 60, 20, []string{"/a"})
	// 纯 move（Buttons()==0）不应触发 FFM
	ev := tcell.NewEventMouse(5, 14, 0, 0, "")
	fm.HandleEvent(ev)
	if fm.focus != 0 {
		t.Errorf("move event in history changed focus: got %d, want 0", fm.focus)
	}
}

// ---- border / separator 不被 hitTest 命中 ----

func TestSession_HitTest_SeparatorMisses(t *testing.T) {
	fm := openTestSession(t, 60, 20, []string{"/a"})
	// history 上分隔线行 = fm.historyY = 12：分隔线本身既不在 listRect (rows 1..11)
	// 也不在 historyRect (rows 13..17) 内，preview 又从 previewX+1=24 起——
	// 点击 (previewX, historyY) 不命中任何 keyboardRegion，focus 不变。
	ev := tcell.NewEventMouse(fm.previewX, fm.historyY, tcell.Button1, 0, "")
	fm.HandleEvent(ev)
	if fm.focus != 0 {
		t.Errorf("click at historyY+sepX: focus=%d, want 0", fm.focus)
	}
}

// ---- ActivateFromHistory ----

func TestSession_ActivateFromHistory_SwitchesFocusToFileList(t *testing.T) {
	fm := openTestSession(t, 60, 20, []string{"/a", "/b"})
	// 真实可切换的目标
	target := t.TempDir()
	if fm.history == nil {
		t.Fatal("history not built")
	}
	// 替换 history 内容（openTestSession 已经写过 /a /b，这里覆盖为 target + /path/b）
	fm.history.dirs = []string{target, "/path/b"}

	// 让 fileList 当前目录临时变到一个不同的目录，确认 chdirTo 真的会切
	otherDir := t.TempDir()
	fm.list.chdirTo(otherDir, "")
	if fm.list.currentDir != otherDir {
		t.Fatalf("precondition: currentDir=%s, want %s", fm.list.currentDir, otherDir)
	}

	// 切到 history 后调用 ActivateFromHistory
	fm.switchFocus(1)
	if fm.focus != 1 || !fm.history.focused {
		t.Fatalf("setup: failed to enter history focus: focus=%d", fm.focus)
	}

	fm.ActivateFromHistory(target)
	if fm.list.currentDir != target {
		t.Errorf("after ActivateFromHistory: currentDir=%s, want %s", fm.list.currentDir, target)
	}
	if fm.focus != 0 {
		t.Errorf("after ActivateFromHistory: focus=%d, want 0 (fileList)", fm.focus)
	}
	if !fm.list.focused {
		t.Errorf("after ActivateFromHistory: fileList.focused=false")
	}
	if fm.history.focused {
		t.Errorf("after ActivateFromHistory: history.focused=true")
	}
}

// ---- close 失焦 ----

func TestSession_CloseLosesFocus(t *testing.T) {
	fm := openTestSession(t, 60, 20, []string{"/a"})
	fm.switchFocus(1)
	if !fm.history.focused {
		t.Fatalf("precondition: history not focused after switchFocus(1)")
	}
	closed := false
	fm.onClose = func(r Result) { closed = true }
	fm.NotifyBlur()
	if !closed {
		t.Errorf("NotifyBlur should call onClose")
	}
}

// ---- 像素验证：history 横线 + label + 交点 ----

func TestSession_Border_HasHistoryLabel(t *testing.T) {
	fm := openTestSession(t, 60, 20, []string{"/a"})
	// 强制渲染一帧
	fm.Display()

	// historyY = 12 行；label 区从 col 0 起：─ ─ [space] R e c e n t [space] P a t h s [space] ─ ─ ...
	// 实际写入位置（label 是 " Recent Paths "）：
	//   col 0: ─
	//   col 1: ─
	//   col 2: ' '（leading space）
	//   col 3..14: R e c e n t [space] P a t h s
	//   col 15: ' '（trailing space）
	//   col 16+: ─ 填充
	//   col previewX=23: ┤ 交点
	if got, _, _, _ := screen.Screen.GetContent(0, fm.historyY); got != '─' {
		t.Errorf("col 0 historyY %d: got %q, want '─'", fm.historyY, got)
	}
	if got, _, _, _ := screen.Screen.GetContent(1, fm.historyY); got != '─' {
		t.Errorf("col 1 historyY %d: got %q, want '─'", fm.historyY, got)
	}
	if got, _, _, _ := screen.Screen.GetContent(2, fm.historyY); got != ' ' {
		t.Errorf("col 2 historyY %d: got %q, want ' ' (leading space)", fm.historyY, got)
	}
	runes := []rune{'R', 'e', 'c', 'e', 'n', 't', ' ', 'P', 'a', 't', 'h', 's'}
	for i, want := range runes {
		got, _, _, _ := screen.Screen.GetContent(3+i, fm.historyY)
		if got != want {
			t.Errorf("at col %d, historyY %d: got %q, want %q", 3+i, fm.historyY, got, want)
		}
	}
	// 交点 (previewX=23, historyY=9) = ┤
	got, _, _, _ := screen.Screen.GetContent(fm.previewX, fm.historyY)
	if got != '┤' {
		t.Errorf("at (previewX=%d, historyY=%d): got %q, want '┤'", fm.previewX, fm.historyY, got)
	}
}

func TestSession_Border_NoHistory_NoSeparatorLabel(t *testing.T) {
	fm := openTestSession(t, 60, 19, nil)
	fm.Display()
	// H=19 < historyMinHeight=20 不构造 history，任何行都不画分隔线 label
	got, _, _, _ := screen.Screen.GetContent(fm.previewX, 12)
	if got == '┤' {
		t.Errorf("H<20 with no history: ┤ should not appear at row 12")
	}
}
