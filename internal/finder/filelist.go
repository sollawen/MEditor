package finder

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/micro-editor/micro/v2/internal/config"
	"github.com/micro-editor/micro/v2/internal/screen"
	"github.com/micro-editor/tcell/v2"
)

// rowKind 分类光标所在行（Enter 上下文语义）。
type rowKind uint8

const (
	rowFile rowKind = iota
	rowBreadcrumb
	rowDir
)

// rightMode 决定每行右侧固定列的内容。
type rightMode uint8

const (
	rightNone rightMode = iota // 不显示右侧列（name 延伸到 scroll 前）
	rightSize                  // 5 列 humanSize
	rightTime                  // 11 列 formatMtime
)

// fileList 是左侧文件列表 region，实现 KeyboardRegion。
// 并发模型：allEntries（含各 gitChar）、showEntries（指针）、isRepo/gitBranch 都是
// fetchGit（后台写）与 display（UI 读）的共享结构，mu（RWMutex）全保护。
type fileList struct {
	fm   *Session // 回引用：syncPreview、close、dialog anchor
	rect Rect     // 内容区矩形（去外框上边框 + 去右分隔符列）；Open 注入，生命周期内不变

	// —— 目录与条目 ——
	currentDir  string
	allEntries  []entry
	showEntries []*entry

	// —— git（后台 goroutine 写、UI 读，mu 保护）——
	isRepo    bool
	gitBranch string
	mu        sync.RWMutex

	// —— 视图状态 ——
	cursor     int
	topIdx     int
	sortMode   sortMode
	sortDesc   bool
	rightMode  rightMode
	showHidden bool
	focused    bool // 当前 keyboardRegions focus（Step 2 起生效）
	pickerW    int  // 内容区宽（截断用），存着省得各处写 rect.W
	listH      int  // 文件列表可见行数（去面包屑行 + hint 行），clamp 滚动时需要
}

// newFileList 构造 fileList region。
func newFileList(fm *Session, rect Rect, cwd, file string, pickerW int) *fileList {
	l := &fileList{
		fm:         fm,
		rect:       rect,
		pickerW:    pickerW,
		listH:      rect.H - 2,
		currentDir: cwd,
		cursor:     0,
		topIdx:     0,
		sortMode:   sortName,
		sortDesc:   false,
		rightMode:  rightSize,
		showHidden: false,
	}
	l.loadDir(cwd, file)
	go l.fetchGit(cwd)
	return l
}

// loadDir 读目录、建条目列表、定位光标。chdir 和构造期各调一次。
func (l *fileList) loadDir(dir, currentFile string) {
	dir = filepath.Clean(dir)
	l.currentDir = dir
	if isHiddenName(currentFile) {
		l.showHidden = true
	}
	l.allEntries = readDirEntries(dir, l.sortMode, l.sortDesc)
	l.rebuildShow()
	l.locate(currentFile)
}

// rebuildShow 从排好序的 allEntries 按 showHidden 过滤，重建 showEntries。
func (l *fileList) rebuildShow() {
	l.showEntries = l.showEntries[:0]
	for i := range l.allEntries {
		if !l.showHidden && isHiddenName(l.allEntries[i].name) {
			continue
		}
		l.showEntries = append(l.showEntries, &l.allEntries[i])
	}
}

// locate 把光标停到当前文件上；找不到 / 无路径 → 首条目。
func (l *fileList) locate(currentFile string) {
	l.cursor = 1
	l.topIdx = 0
	if currentFile == "" {
		return
	}
	for i, e := range l.showEntries {
		if e.name == currentFile {
			l.cursor = i + 1
			return
		}
	}
}

// —— NoKeyboardRegion ——

func (l *fileList) Rect() Rect { return l.rect }

func (l *fileList) Display() {
	l.drawContent()
}

func (l *fileList) HandleMouse(ev *tcell.EventMouse) bool {
	return l.handleLeftMouse(ev)
}

// —— KeyboardRegion ——

func (l *fileList) FocusOn() {
	l.focused = true
	screen.Redraw()
}

func (l *fileList) FocusLost() {
	l.focused = false
	screen.Redraw()
}

func (l *fileList) HandleKey(ev *tcell.EventKey) bool {
	switch ev.Key() {
	case tcell.KeyDown:
		l.moveCursor(+1)
		return true
	case tcell.KeyUp:
		l.moveCursor(-1)
		return true
	case tcell.KeyEnter:
		l.activate()
		return true
	case tcell.KeyLeft:
		l.chdirParent()
		return true
	case tcell.KeyRight:
		if l.cursorIsDir() {
			l.chdir(l.showEntries[l.cursor-1].name)
		}
		return true
	case tcell.KeyRune:
		switch ev.Rune() {
		case '.':
			l.toggleHidden()
			return true
		case 'd':
			l.startDelete()
			return true
		case 'a':
			l.startAdd()
			return true
		case 'r':
			l.startRename()
			return true
		}
		return false
	}
	return false
}

// ---- 视图：内容区三段布局（面包屑 / 文件列表 / hint） ----

func (l *fileList) drawContent() {
	revStyle := config.DefStyle.Reverse(true)

	x := l.rect.X
	y := l.rect.Y
	w := l.rect.W
	h := l.rect.H

	listTop := y + 1
	listH := l.listH
	if listH < 0 {
		listH = 0
	}

	// —— 锁内：读 isRepo + cursor/topIdx + 拷出可见 entry 值 ——
	l.mu.RLock()
	gitOn := l.isRepo
	cursorOnBc := l.cursor == 0
	total := len(l.showEntries)
	visibleH := min(total, listH)
	vis := make([]entry, visibleH)
	for vi := 0; vi < visibleH; vi++ {
		i := l.topIdx + vi
		if i >= total {
			break
		}
		vis[vi] = *l.showEntries[i]
	}
	cursor, topIdx := l.cursor, l.topIdx
	l.mu.RUnlock()

	// 行 0：面包屑
	bcStyle := config.GetColor("type")
	if l.focused && cursorOnBc {
		bcStyle = revStyle
	}
	l.drawBreadcrumb(x, y, w, bcStyle)

	// —— 锁外：逐行画 ——
	for vi := 0; vi < visibleH; vi++ {
		l.drawEntry(x, listTop+vi, w, vis[vi], gitOn, l.focused && topIdx+vi+1 == cursor, revStyle)
	}

	// 滚动指示符
	scrollCol := x + w - 1
	if gitOn {
		scrollCol = x + w - 2
	}
	if total > visibleH && visibleH > 0 {
		topStyle := config.DefStyle
		if l.focused && topIdx+1 == cursor {
			topStyle = revStyle
		}
		botStyle := config.DefStyle
		if l.focused && topIdx+visibleH == cursor {
			botStyle = revStyle
		}
		if topIdx > 0 {
			screen.Screen.SetContent(scrollCol, listTop, '▲', nil, topStyle)
		}
		if topIdx+visibleH < total {
			screen.Screen.SetContent(scrollCol, listTop+visibleH-1, '▼', nil, botStyle)
		}
	}

	// 末行：perms + size + mtime
	hintRow := y + h - 1
	text := l.buildMetaLine(w)
	drawString(x, hintRow, w, text, config.DefStyle)
}

// drawBreadcrumb 画面包屑行（左截断全路径，恒保留"当前目录/"；根目录显 /）。
func (l *fileList) drawBreadcrumb(x, y, w int, style tcell.Style) {
	var path string
	switch {
	case l.currentDir == "" || l.currentDir == string(filepath.Separator):
		path = string(filepath.Separator)
	default:
		path = l.currentDir
		if !strings.HasSuffix(path, string(filepath.Separator)) {
			path += string(filepath.Separator)
		}
	}
	path = shortenHomePath(path)
	disp := truncateLeftPath(path, w)
	col := x
	for _, r := range disp {
		rw := runeWidth(r)
		if col+rw > x+w {
			break
		}
		screen.Screen.SetContent(col, y, r, nil, style)
		col += rw
	}
	for col < x+w {
		screen.Screen.SetContent(col, y, ' ', nil, style)
		col++
	}
}

// drawEntry 画一个文件/目录条目行。
func (l *fileList) drawEntry(x, y, w int, e entry, gitOn bool, selected bool, revStyle tcell.Style) {
	const sizeW, timeW = 5, 11
	R := x + w

	var gitCol, scrollCol int
	if gitOn {
		gitCol, scrollCol = R-1, R-2
	} else {
		scrollCol = R - 1
	}
	rightW := 0
	switch l.rightMode {
	case rightSize:
		rightW = sizeW
	case rightTime:
		rightW = timeW
	}
	rightStart := scrollCol
	nameLimit := scrollCol - (x + 1)
	if rightW > 0 {
		rightStart = scrollCol - rightW
		nameLimit = rightStart - (x + 1) - 1
	}
	if nameLimit < 0 {
		nameLimit = 0
	}
	nameCap := rightStart
	if rightW == 0 {
		nameCap = scrollCol
	}

	// 颜色
	nameStyle := config.DefStyle
	if e.isDir {
		nameStyle = config.GetColor("type")
	}
	fillStyle := config.DefStyle
	gitStyle := gitCharStyle(e.gitChar)
	if selected {
		nameStyle, fillStyle, gitStyle = revStyle, revStyle, revStyle
	}

	// marker
	col := x
	marker := ' '
	if e.isDir {
		marker = '▸'
	}
	screen.Screen.SetContent(col, y, marker, nil, nameStyle)
	col += runeWidth(marker)

	// name（截断）+ 填充到 nameCap
	dispName := e.name
	if e.isDir {
		dispName += string(filepath.Separator)
	}
	dispName = truncateNameKeepExt(dispName, nameLimit, e.isDir)
	for _, r := range dispName {
		rw := runeWidth(r)
		if col+rw > nameCap {
			break
		}
		screen.Screen.SetContent(col, y, r, nil, nameStyle)
		col += rw
	}
	for col < nameCap {
		screen.Screen.SetContent(col, y, ' ', nil, fillStyle)
		col++
	}

	// right 区
	if rightW > 0 {
		for c := rightStart; c < scrollCol; c++ {
			screen.Screen.SetContent(c, y, ' ', nil, fillStyle)
		}
		if !e.isDir {
			var rs string
			if l.rightMode == rightTime {
				rs = formatMtime(e.info.ModTime())
			} else {
				rs = humanSize(e.info.Size())
			}
			sw := stringWidth(rs)
			c := scrollCol - sw
			if c < rightStart {
				c = rightStart
			}
			for _, r := range rs {
				screen.Screen.SetContent(c, y, r, nil, fillStyle)
				c += runeWidth(r)
			}
		}
	}

	// scroll 列
	screen.Screen.SetContent(scrollCol, y, ' ', nil, fillStyle)

	// git 列
	if gitOn {
		if e.gitChar != 0 {
			screen.Screen.SetContent(gitCol, y, e.gitChar, nil, gitStyle)
		} else {
			screen.Screen.SetContent(gitCol, y, ' ', nil, fillStyle)
		}
	}
}

// buildMetaLine 组装光标条目的元数据行（perms + size + mtime）。
func (l *fileList) buildMetaLine(w int) string {
	idx := l.cursor - 1
	if idx < 0 || idx >= len(l.showEntries) {
		return ""
	}
	fi := l.showEntries[idx].info
	perms := fi.Mode().String()
	size := humanSize(fi.Size())
	mtime := formatMtime(fi.ModTime())
	return fitMeta(w, perms, size, mtime)
}

// ---- 鼠标 ----

func (l *fileList) handleLeftMouse(ev *tcell.EventMouse) bool {
	switch ev.Buttons() {
	case tcell.Button1:
		_, my := ev.Position()
		target, ok := l.listRowAt(my)
		if !ok {
			return false
		}
		if target == l.cursor {
			return true
		}
		l.moveCursor(target - l.cursor)
		return true
	case tcell.WheelUp:
		l.moveCursor(-1)
		return true
	case tcell.WheelDown:
		l.moveCursor(1)
		return true
	}
	return false
}

// listRowAt 把屏坐标 my 映射成目标 cursor 值。
func (l *fileList) listRowAt(my int) (cursor int, ok bool) {
	bcRow := l.rect.Y
	listTop := l.rect.Y + 1
	if my == bcRow {
		return 0, true
	}
	visibleH := len(l.showEntries)
	if visibleH > l.listH {
		visibleH = l.listH
	}
	if my >= listTop && my < listTop+visibleH {
		return l.topIdx + (my - listTop) + 1, true
	}
	return 0, false
}

// ---- 光标移动与导航 ----

func (l *fileList) moveCursor(delta int) {
	max := len(l.showEntries)
	l.cursor += delta
	if l.cursor < 0 {
		l.cursor = 0
	}
	if l.cursor > max {
		l.cursor = max
	}
	l.ensureVisible()
	l.fm.syncPreview()
	screen.Redraw()
}

func (l *fileList) activate() {
	switch l.cursorRowKind() {
	case rowBreadcrumb:
		l.chdirParent()
	case rowDir:
		if l.cursor >= 1 && l.cursor-1 < len(l.showEntries) {
			l.chdir(l.showEntries[l.cursor-1].name)
		}
	case rowFile:
		// 选文件分支：先校验当前目录依然存在且是目录，否则直接返回不记录也不关闭
		if info, err := os.Stat(l.currentDir); err != nil || !info.IsDir() {
			return
		}
		sep := string(filepath.Separator)
		dir := l.currentDir
		if !strings.HasSuffix(dir, sep) {
			dir += sep
		}
		writeHistory(dir)
		l.pick()
	}
}

func (l *fileList) pick() {
	if l.cursorIsDir() {
		return
	}
	idx := l.cursor - 1
	if idx < 0 || idx >= len(l.showEntries) {
		return
	}
	l.fm.closePicked(l.showEntries[idx].name)
}

func (l *fileList) chdirParent() {
	parent := filepath.Dir(l.currentDir)
	if parent == l.currentDir {
		return
	}
	l.chdirTo(parent, filepath.Base(l.currentDir))
}

func (l *fileList) chdir(sub string) {
	l.chdirTo(filepath.Join(l.currentDir, sub), "")
}

func (l *fileList) chdirTo(target, focusName string) {
	target = filepath.Clean(target)
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		return
	}
	l.mu.Lock()
	l.currentDir = target
	l.allEntries = readDirEntries(target, l.sortMode, l.sortDesc)
	l.rebuildShow()
	l.topIdx = 0
	l.cursor = 1
	if focusName != "" {
		for i, e := range l.showEntries {
			if e.name == focusName {
				l.cursor = i + 1
				break
			}
		}
	}
	if len(l.showEntries) == 0 {
		l.cursor = 0
	}
	l.ensureVisible()
	l.mu.Unlock()

	l.fm.syncPreview()
	screen.Redraw()
	go l.fetchGit(target)
}

func (l *fileList) toggleHidden() {
	l.mu.Lock()
	oldName := ""
	oldIdx := l.cursor - 1
	if l.cursor >= 1 && l.cursor-1 < len(l.showEntries) {
		oldName = l.showEntries[l.cursor-1].name
	}
	l.showHidden = !l.showHidden
	l.rebuildShow()
	found := false
	if oldName != "" {
		for i, e := range l.showEntries {
			if e.name == oldName {
				l.cursor = i + 1
				found = true
				break
			}
		}
	}
	if !found {
		idx := oldIdx
		if idx >= len(l.showEntries) {
			idx = len(l.showEntries) - 1
		}
		if idx < 0 {
			idx = 0
		}
		if len(l.showEntries) == 0 {
			l.cursor = 0
		} else {
			l.cursor = idx + 1
		}
	}
	l.ensureVisible()
	l.mu.Unlock()

	l.fm.syncPreview()
	screen.Redraw()
}

func (l *fileList) cursorRowKind() rowKind {
	if l.cursor == 0 {
		return rowBreadcrumb
	}
	idx := l.cursor - 1
	if idx < 0 || idx >= len(l.showEntries) {
		return rowFile
	}
	if l.showEntries[idx].isDir {
		return rowDir
	}
	return rowFile
}

func (l *fileList) cursorIsDir() bool {
	if l.cursor < 1 || l.cursor-1 >= len(l.showEntries) {
		return false
	}
	return l.showEntries[l.cursor-1].isDir
}

func (l *fileList) ensureVisible() {
	if l.cursor == 0 {
		return
	}
	idx := l.cursor - 1
	listH := l.listH
	if listH <= 0 {
		return
	}
	if idx < l.topIdx {
		l.topIdx = idx
	}
	if idx >= l.topIdx+listH {
		l.topIdx = idx - listH + 1
	}
}

// selectedFilePath 返回当前光标文件的完整路径，供 syncPreview 喂给 preview。
func (l *fileList) selectedFilePath() string {
	cur := l.cursor
	if cur <= 0 || cur > len(l.showEntries) {
		return ""
	}
	e := l.showEntries[cur-1]
	if e.isDir || e.name == "" {
		return ""
	}
	return filepath.Join(l.currentDir, e.name)
}

// ---- git ----

func (l *fileList) fetchGit(dir string) {
	isRepo, branch, chars, state := getGitStatus(dir)
	l.mu.Lock()
	if l.currentDir != dir {
		l.mu.Unlock()
		return
	}
	l.isRepo = isRepo
	l.gitBranch = branch
	for i := range l.allEntries {
		// chars 优先于 dir 级标志：untracked 目录内仍可能有个别 ignored 条目
		// （--ignored=traditional 会逐个列出 ?? 目录里的 !! 文件），须显示 I 而非 U。
		if ch, ok := chars[l.allEntries[i].name]; ok {
			l.allEntries[i].gitChar = ch
			continue
		}
		switch state {
		case dirAllIgnored:
			l.allEntries[i].gitChar = 'I'
		case dirAllUntracked:
			l.allEntries[i].gitChar = 'U'
		}
	}
	l.mu.Unlock()
	screen.Redraw()
}

// gitCharStyle 把 git 状态字符映射成 colorscheme 颜色。
func gitCharStyle(c rune) tcell.Style {
	switch c {
	case 'M', 'R':
		return config.GetColor("diff-modified")
	case 'A', 'U':
		return config.GetColor("diff-added")
	case 'D':
		return config.GetColor("diff-deleted")
	}
	return config.DefStyle
}

// min 是内建的极简版本。
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}