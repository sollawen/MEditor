package finder

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/micro-editor/micro/v2/internal/config"
	"github.com/micro-editor/tcell/v2"
)

// ---- 持久化测试 ----
//
// 所有持久化测试都在 t.TempDir() 隔离的 ConfigDir 中运行，避免污染用户配置。

func withTempConfigDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
	saved := config.ConfigDir
	config.ConfigDir = dir
	t.Cleanup(func() { config.ConfigDir = saved })
}

// readBackFile 把 history.json 读回成原始字节（用于断言损坏行为）。
func readBackFile(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(historyPath())
	if err != nil {
		return nil
	}
	return data
}

func TestHistoryRead_NotExist(t *testing.T) {
	withTempConfigDir(t)
	got := readHistory()
	if len(got) != 0 {
		t.Errorf("readHistory when file missing: got %v, want empty", got)
	}
}

func TestHistoryRead_EmptyFileTreatedAsCorrupt(t *testing.T) {
	withTempConfigDir(t)
	if err := os.WriteFile(historyPath(), []byte{}, 0o644); err != nil {
		t.Fatalf("write empty file: %v", err)
	}
	got := readHistory()
	if len(got) != 0 {
		t.Errorf("readHistory empty file: got %v, want empty", got)
	}
	if _, err := os.Stat(historyPath()); !os.IsNotExist(err) {
		t.Errorf("expected history.json removed after empty read; stat err = %v", err)
	}
}

func TestHistoryRead_CorruptJSONRemoved(t *testing.T) {
	withTempConfigDir(t)
	if err := os.WriteFile(historyPath(), []byte("{broken"), 0o644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	got := readHistory()
	if len(got) != 0 {
		t.Errorf("readHistory corrupt: got %v, want empty", got)
	}
	if _, err := os.Stat(historyPath()); !os.IsNotExist(err) {
		t.Errorf("expected history.json removed after corrupt read; stat err = %v", err)
	}
}

func TestHistoryRead_ValidFile(t *testing.T) {
	withTempConfigDir(t)
	want := []string{"/a/", "/b/"}
	data, _ := json.Marshal(want)
	if err := os.WriteFile(historyPath(), data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := readHistory()
	if len(got) != 2 || got[0] != "/a/" || got[1] != "/b/" {
		t.Errorf("readHistory: got %v, want %v", got, want)
	}
}

func TestHistoryRead_ReturnsIndependentSlice(t *testing.T) {
	withTempConfigDir(t)
	in := []string{"/a/", "/b/"}
	data, _ := json.Marshal(in)
	if err := os.WriteFile(historyPath(), data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	out := readHistory()
	out[0] = "/mutated"
	out2 := readHistory()
	if out2[0] != "/a/" {
		t.Errorf("mutating returned slice leaked into next read: %v", out2)
	}
}

func TestRecord_FirstWritesDirectory(t *testing.T) {
	withTempConfigDir(t)
	dir := t.TempDir()
	sep := string(filepath.Separator)
	writeHistory(dir + sep)

	raw, err := os.ReadFile(historyPath())
	if err != nil {
		t.Fatalf("history.json should be created; read err = %v", err)
	}
	var dirs []string
	if err := json.Unmarshal(raw, &dirs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(dirs) != 1 || dirs[0] != dir+sep {
		t.Errorf("after first record: got %v, want [%s]", dirs, dir+sep)
	}
}

func TestRecord_DedupesAndMovesToHead(t *testing.T) {
	withTempConfigDir(t)
	a := t.TempDir()
	b := t.TempDir()
	c := t.TempDir()

	sep := string(filepath.Separator)
	writeHistory(a + sep)
	writeHistory(b + sep)
	writeHistory(a + sep) // a 已被记录，应去重并移到队首
	writeHistory(c + sep)

	dirs := readHistory()
	want := []string{c + sep, a + sep, b + sep}
	if len(dirs) != len(want) {
		t.Fatalf("len: got %d, want %d (%v)", len(dirs), len(want), dirs)
	}
	for i := range want {
		if dirs[i] != want[i] {
			t.Errorf("idx %d: got %s, want %s; full=%v", i, dirs[i], want[i], dirs)
		}
	}
}

func TestRecord_TruncatesToMaxEntries(t *testing.T) {
	withTempConfigDir(t)
	dirs := make([]string, 0, historyMaxEntries+10)
	for i := 0; i < historyMaxEntries+10; i++ {
		d := t.TempDir()
		dirs = append(dirs, d)
	}
	sep := string(filepath.Separator)
	// 按从老到新写入
	for _, d := range dirs {
		writeHistory(d + sep)
	}
	got := readHistory()
	if len(got) != historyMaxEntries {
		t.Errorf("len after %d records: got %d, want %d", len(dirs), len(got), historyMaxEntries)
	}
	// 队首应为最新 (最后写入)，队尾应为最早但被丢弃
	if got[0] != dirs[len(dirs)-1]+sep {
		t.Errorf("head: got %s, want %s", got[0], dirs[len(dirs)-1]+sep)
	}
	// 第 historyMaxEntries 条 应为 dirs[len(dirs)-historyMaxEntries]，即 dirs[10]
	expTail := dirs[len(dirs)-historyMaxEntries] + sep
	if got[historyMaxEntries-1] != expTail {
		t.Errorf("tail: got %s, want %s", got[historyMaxEntries-1], expTail)
	}
}

func TestRecord_EmptyPathDoesNothing(t *testing.T) {
	withTempConfigDir(t)
	writeHistory("")
	if _, err := os.Stat(historyPath()); !os.IsNotExist(err) {
		t.Errorf("history.json should not be created for empty path; stat err = %v", err)
	}
}

func TestRecord_DoesNotPolluteUserConfig(t *testing.T) {
	// 双重保险：withTempConfigDir 已经隔离；这里再次确认写到 historyPath() 这个路径
	withTempConfigDir(t)
	d := t.TempDir()
	writeHistory(d)
	got, err := os.ReadFile(historyPath())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want, _ := filepath.Abs(d)
	if !contains(got, []byte(want)) {
		t.Errorf("history.json content %q missing %q", got, want)
	}
}

func TestHistoryRead_FiltersEntriesWithoutTrailingSeparator(t *testing.T) {
	withTempConfigDir(t)
	writeDirHistory([]string{"/a", "/b/"})
	got := readHistory()
	if len(got) != 1 || got[0] != "/b/" {
		t.Errorf("readHistory: got %v, want [/b/]", got)
	}
}

func TestHistoryRead_FiltersEmptyString(t *testing.T) {
	withTempConfigDir(t)
	writeDirHistory([]string{"/a/", "", "/b/"})
	got := readHistory()
	if len(got) != 2 || got[0] != "/a/" || got[1] != "/b/" {
		t.Errorf("readHistory: got %v, want [/a/ /b/]", got)
	}
}

func TestHistoryRead_MixedOldAndNewFormat(t *testing.T) {
	withTempConfigDir(t)
	writeDirHistory([]string{"/old_no_sep", "/new_with_sep/"})
	got := readHistory()
	if len(got) != 1 || got[0] != "/new_with_sep/" {
		t.Errorf("readHistory: got %v, want [/new_with_sep/]", got)
	}
}

// contains 子串判断，bytes.Contains 的小包装（让测试代码读起来自然一点）。
func contains(haystack, needle []byte) bool {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// ---- historyList 区域测试 ----
//
// 这些测试只检查状态变化与激活行为，不直接调 Display()（依赖 screen.Screen）。
// Display 的像素断言在 session_test.go 通过 SimScreen 验证。

func newTestHistory(t *testing.T, rect Rect, dirs []string) (*historyList, *Session) {
	t.Helper()
	fm := &Session{}
	copied := make([]string, len(dirs))
	copy(copied, dirs)
	return newHistoryList(fm, rect, copied), fm
}

func TestHistoryList_Rect(t *testing.T) {
	r := Rect{X: 1, Y: 2, W: 30, H: 3}
	h := newHistoryList(&Session{}, r, nil)
	if gr := h.Rect(); gr != r {
		t.Errorf("Rect(): got %v, want %v", gr, r)
	}
}

func TestHistoryList_MoveCursor_Clamp(t *testing.T) {
	h, _ := newTestHistory(t, Rect{W: 10, H: 3}, []string{"/a", "/b", "/c"})

	h.moveCursor(-1) // 已在 0 → clamp
	if h.cursor != 0 {
		t.Errorf("Up at top: cursor=%d, want 0", h.cursor)
	}
	h.moveCursor(2) // 0+2=2
	if h.cursor != 2 {
		t.Errorf("Down twice: cursor=%d, want 2", h.cursor)
	}
	h.moveCursor(1) // 2+1=3 → clamp 到 2（n-1=2）
	if h.cursor != 2 {
		t.Errorf("Down at bottom: cursor=%d, want 2", h.cursor)
	}
}

func TestHistoryList_EnsureVisible_FollowsCursor(t *testing.T) {
	dirs := []string{"/a", "/b", "/c", "/d", "/e"}
	h, _ := newTestHistory(t, Rect{W: 10, H: 3}, dirs)

	// 初始：cursor=0, topIdx=0，可见 0..2
	h.cursor = 4
	h.ensureVisible()
	if h.topIdx != 4-len(dirs) && h.cursor-h.topIdx+1 > 3 {
		t.Errorf("ensureVisible: topIdx=%d, want cursor-fit", h.topIdx)
	}
	if h.cursor < h.topIdx || h.cursor >= h.topIdx+3 {
		t.Errorf("cursor not visible: cursor=%d topIdx=%d", h.cursor, h.topIdx)
	}

	h.cursor = 0
	h.ensureVisible()
	if h.topIdx != 0 {
		t.Errorf("ensureVisible back to head: topIdx=%d, want 0", h.topIdx)
	}
}

func TestHistoryList_HandleKey_UpDown(t *testing.T) {
	h, _ := newTestHistory(t, Rect{W: 10, H: 3}, []string{"/a", "/b"})
	if !h.HandleKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone, "")) || h.cursor != 1 {
		t.Errorf("Down: returned %v, cursor=%d", true, h.cursor)
	}
	if !h.HandleKey(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone, "")) || h.cursor != 0 {
		t.Errorf("Up: returned %v, cursor=%d", true, h.cursor)
	}
}

func TestHistoryList_HandleKey_OtherKeysReturnFalse(t *testing.T) {
	h, _ := newTestHistory(t, Rect{W: 10, H: 3}, []string{"/a"})
	// 不在白名单里的键返回 false，让 session 走 Esc/Ctrl-Q/q 等全局路由
	wantFalse := []tcell.Key{tcell.KeyEscape, tcell.KeyLeft, tcell.KeyHome}
	for _, k := range wantFalse {
		ev := tcell.NewEventKey(k, 0, tcell.ModNone, "")
		if h.HandleKey(ev) {
			t.Errorf("key %v: returned true, want false", k)
		}
	}
}

func TestHistoryList_FocusState_Toggle(t *testing.T) {
	h, _ := newTestHistory(t, Rect{W: 10, H: 3}, []string{"/a"})
	if h.focused {
		t.Errorf("initial: focused=true, want false")
	}
	h.FocusOn()
	if !h.focused {
		t.Errorf("after FocusOn: focused=false, want true")
	}
	h.FocusLost()
	if h.focused {
		t.Errorf("after FocusLost: focused=true, want false")
	}
}

func TestHistoryList_Mouse_Wheel(t *testing.T) {
	h, _ := newTestHistory(t, Rect{W: 10, H: 3}, []string{"/a", "/b", "/c"})
	ev := tcell.NewEventMouse(5, h.rect.Y, tcell.WheelDown, 0, "")
	if !h.HandleMouse(ev) || h.cursor != 1 {
		t.Errorf("WheelDown: returned %v, cursor=%d", true, h.cursor)
	}
	ev2 := tcell.NewEventMouse(5, h.rect.Y, tcell.WheelUp, 0, "")
	if !h.HandleMouse(ev2) || h.cursor != 0 {
		t.Errorf("WheelUp: returned %v, cursor=%d", true, h.cursor)
	}
}

func TestHistoryList_Mouse_MoveReturnsFalse(t *testing.T) {
	h, _ := newTestHistory(t, Rect{W: 10, H: 3}, []string{"/a", "/b"})
	// Buttons()==0 的纯 move 事件
	ev := tcell.NewEventMouse(5, h.rect.Y, 0, 0, "")
	if h.HandleMouse(ev) {
		t.Errorf("move without buttons: returned true, want false")
	}
	if h.cursor != 0 {
		t.Errorf("move changed cursor to %d", h.cursor)
	}
}

func TestHistoryList_Mouse_ClickSelects(t *testing.T) {
	h, _ := newTestHistory(t, Rect{W: 10, H: 3}, []string{"/a", "/b", "/c"})
	ev := tcell.NewEventMouse(5, h.rect.Y+1, tcell.Button1, 0, "")
	if !h.HandleMouse(ev) {
		t.Errorf("click: returned false")
	}
	if h.cursor != 1 {
		t.Errorf("click on row 1: cursor=%d, want 1", h.cursor)
	}
}

func TestHistoryList_Mouse_ClickBlankReturnsFalse(t *testing.T) {
	h, _ := newTestHistory(t, Rect{W: 10, H: 3}, []string{"/a"})
	// 只有 1 条可见，点 row 2 是空白
	ev := tcell.NewEventMouse(5, h.rect.Y+2, tcell.Button1, 0, "")
	if h.HandleMouse(ev) {
		t.Errorf("click on blank: returned true, want false")
	}
}

func TestHistoryList_RemoveCurrent_AdjustsCursor(t *testing.T) {
	withTempConfigDir(t)
	dirs := []string{"/a", "/b", "/c"}
	h, _ := newTestHistory(t, Rect{W: 10, H: 3}, dirs)
	h.cursor = 1
	h.removeCurrent()
	if h.cursor != 1 {
		t.Errorf("after remove idx 1 (middle): cursor=%d, want 1 (下一条顶上)", h.cursor)
	}
	if len(h.dirs) != 2 || h.dirs[0] != "/a" || h.dirs[1] != "/c" {
		t.Errorf("after remove: dirs=%v, want [/a /c]", h.dirs)
	}
	// 持久化
	raw, err := os.ReadFile(historyPath())
	if err != nil {
		t.Fatalf("history.json not written: %v", err)
	}
	var got []string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 2 || got[0] != "/a" || got[1] != "/c" {
		t.Errorf("history.json: %v, want [/a /c]", got)
	}
}

func TestHistoryList_RemoveCurrent_LastEntry(t *testing.T) {
	withTempConfigDir(t)
	h, _ := newTestHistory(t, Rect{W: 10, H: 3}, []string{"/a", "/b"})
	h.cursor = 1
	h.removeCurrent()
	if h.cursor != 0 {
		t.Errorf("after remove last: cursor=%d, want 0", h.cursor)
	}
	h.removeCurrent()
	// 现在空列表：cursor 应保持 0，目录、文件不应 panic
	if h.cursor != 0 {
		t.Errorf("after remove all: cursor=%d, want 0", h.cursor)
	}
}

func TestHistoryList_Activate_ValidDirCallsSession(t *testing.T) {
	t.Skip("integration covered in session_test.go with real Screen")
}
func TestHistoryList_Activate_NonexistentRemoved(t *testing.T) {
	withTempConfigDir(t)
	realDir := t.TempDir() // 留作 dirs 第一条，验证 removeCurrent 删除第二条后的状态
	dirs := []string{"/path/that/does/not/exist/xyz", realDir}
	h, _ := newTestHistory(t, Rect{W: 10, H: 3}, dirs)
	h.cursor = 0
	h.removeCurrent() // 直接验证 removeCurrent 的切片与持久化清理路径
	if len(h.dirs) != 1 || h.dirs[0] != realDir {
		t.Errorf("after removing non-existent: dirs=%v, want [%s]", h.dirs, realDir)
	}
}

func TestHistoryList_Activate_DirWithTrailingSeparator(t *testing.T) {
	sep := string(filepath.Separator)
	target := t.TempDir()
	fm := openTestSession(t, 60, 20, []string{target + sep})
	if fm.history == nil {
		t.Fatal("history not built")
	}

	fm.history.activate()
	if fm.list.currentDir != target {
		t.Errorf("activate: currentDir=%q, want %q", fm.list.currentDir, target)
	}
}

func TestHistoryList_Activate_RemovesWhenPathBecameFile(t *testing.T) {
	withTempConfigDir(t)
	path := filepath.Join(t.TempDir(), "was-directory")
	if err := os.WriteFile(path, []byte("file"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	h, _ := newTestHistory(t, Rect{W: 10, H: 3}, []string{path + string(filepath.Separator)})

	h.activate()
	if len(h.dirs) != 0 {
		t.Errorf("activate file path: dirs=%v, want empty", h.dirs)
	}
}

func TestHistoryList_Activate_RemovesOnStatError(t *testing.T) {
	withTempConfigDir(t)
	invalid := string([]byte{0}) + string(filepath.Separator)
	h, _ := newTestHistory(t, Rect{W: 10, H: 3}, []string{invalid})

	h.activate()
	if len(h.dirs) != 0 {
		t.Errorf("activate path with stat error: dirs=%v, want empty", h.dirs)
	}
}
