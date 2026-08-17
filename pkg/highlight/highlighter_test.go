package highlight

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// realDefs 从 ../../runtime/syntax 加载全部真实 yaml 并 ResolveIncludes，
// 返回 filetype → Def。整个测试包只加载一次：Groups 是包级全局表，
// 重复 ParseDef 会重复占用组号，共享实例才能跨用例比较 Group 值。
var (
	defsOnce sync.Once
	defs     map[string]*Def
	defsErr  error
)

func realDefs(t *testing.T) map[string]*Def {
	t.Helper()
	defsOnce.Do(func() {
		dir := "../../runtime/syntax"
		entries, err := os.ReadDir(dir)
		if err != nil {
			defsErr = err
			return
		}
		defs = map[string]*Def{}
		var files []*File
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			pf, err := ParseFile(data)
			if err != nil {
				continue
			}
			hdr, err := MakeHeaderYaml(data)
			if err != nil {
				continue
			}
			d, err := ParseDef(pf, hdr)
			if err != nil {
				continue
			}
			defs[pf.FileType] = d
			files = append(files, pf)
		}
		for _, d := range defs {
			ResolveIncludes(d, files)
		}
	})
	if defsErr != nil {
		t.Skipf("runtime/syntax 不可读，跳过: %v", defsErr)
	}
	return defs
}

// testLineStates 是内存版 LineStates，供 HighlightStates/HighlightMatches 用。
type testLineStates struct {
	lines   []string
	states  []State
	matches []LineMatch
}

func newTestLineStates(lines []string) *testLineStates {
	return &testLineStates{
		lines:   lines,
		states:  make([]State, len(lines)),
		matches: make([]LineMatch, len(lines)),
	}
}

func (b *testLineStates) LineBytes(n int) []byte      { return []byte(b.lines[n]) }
func (b *testLineStates) LinesNum() int               { return len(b.lines) }
func (b *testLineStates) State(n int) State           { return b.states[n] }
func (b *testLineStates) SetState(n int, s State)     { b.states[n] = s }
func (b *testLineStates) SetMatch(n int, m LineMatch) { b.matches[n] = m }
func (b *testLineStates) Lock()                       {}
func (b *testLineStates) Unlock()                     {}

// hasGroup 护断 LineMatch 中是否存在名为 name 的组。
func hasGroup(m LineMatch, name string) bool {
	for _, g := range m {
		if g.String() == name {
			return true
		}
	}
	return false
}

// t1Line159 是 T1 文档 159 行原文：正则字符类 /["}']/g 内含落单引号，
// 原版引擎会把 " 当字符串 region start 开启，污染后续所有行。
const t1Line159 = `    const txt = String(obj.chatTxt ?? '').replace(/["}']/g, '').trim().slice(0, 20);`

// TestPatternShieldsRegionStart 顶层 .ts 单行：正则字面量字符类内的引号
// 被 constant pattern extent 遮蔽，不再开启 string region，行末回到顶层。
func TestPatternShieldsRegionStart(t *testing.T) {
	def := realDefs(t)["typescript"]
	if def == nil {
		t.Fatal("typescript def 未加载")
	}
	h := NewHighlighter(def)
	matches := h.HighlightString(`const x = /["}']/g`)

	if len(matches) != 1 {
		t.Fatalf("HighlightString 返回 %d 行, want 1", len(matches))
	}
	m := matches[0]
	if !hasGroup(m, "constant") {
		t.Errorf("Match 应含正则字面量的 constant 组, got %v", m)
	}
	if hasGroup(m, "constant.string") {
		t.Errorf("整行无合法字符串，不应出现 constant.string 组, got %v", m)
	}
	if h.lastRegion != nil {
		t.Errorf("行末 state 应为 nil(顶层), got %v", h.lastRegion.group)
	}
}

// TestRegionStartUnshielded 真·未闭合字符串：引号不被任何 pattern 遮蔽，
// string region 照常开启（与 VSCode 一致），本修复不改变该行为。
func TestRegionStartUnshielded(t *testing.T) {
	def := realDefs(t)["typescript"]
	if def == nil {
		t.Fatal("typescript def 未加载")
	}
	h := NewHighlighter(def)
	matches := h.HighlightString(`const s = "abc`)

	if len(matches) != 1 {
		t.Fatalf("HighlightString 返回 %d 行, want 1", len(matches))
	}
	if h.lastRegion == nil {
		t.Error("未闭合字符串应开启 string region, state 不应为 nil")
	} else if h.lastRegion.group.String() != "constant.string" {
		t.Errorf("state 应为 constant.string region, got %s", h.lastRegion.group)
	}
	if !hasGroup(matches[0], "constant.string") {
		t.Errorf("Match 应含 string 组, got %v", matches[0])
	}
}

// TestShieldInsideFence md 文档 ```ts fence 内的 T1:159 行：
// 块内行恢复正常 ts 语法组（正则 constant 出现），闭 fence 后 state 归 nil。
func TestShieldInsideFence(t *testing.T) {
	def := realDefs(t)["markdown"]
	if def == nil {
		t.Fatal("markdown def 未加载")
	}
	lines := []string{"```ts", t1Line159, "```"}
	h := NewHighlighter(def)
	matches := h.HighlightString(strings.Join(lines, "\n"))

	if len(matches) != 3 {
		t.Fatalf("HighlightString 返回 %d 行, want 3", len(matches))
	}
	m := matches[1]
	if !hasGroup(m, "constant") {
		t.Errorf("块内行 Match 应含正则 constant 组, got %v", m)
	}
	// 列 38-39 的 '' 是合法配对字符串，不算污染；污染特征是列 50 的落单 "
	// 开启 string 染到行尾。断言：列 50 及之后不再有 constant.string 组。
	lastStr := -1
	for i, g := range m {
		if g.String() == "constant.string" && i >= 50 {
			t.Errorf("列 %d 出现 constant.string，落单引号未被遮蔽: %v", i, m)
		}
		if g.String() == "constant.string" {
			lastStr = i
		}
	}
	if lastStr > 40 {
		t.Errorf("最后一个 constant.string 在列 %d，应仅限列 38-39 的配对: %v", lastStr, m)
	}
	if h.lastRegion != nil {
		t.Errorf("闭 fence 后 state 应为 nil, got %v", h.lastRegion.group)
	}
}

// TestShieldStatesMatchesConsistent statesOnly(HighlightStates) 与
// 非 statesOnly(HighlightString / HighlightMatches) 两条入口必须看到
// 相同的 region 开启决策：闭 fence 后 state 均为 nil，且两条入口产出的
// Match 逐行一致，否则 state 与颜色错位。
func TestShieldStatesMatchesConsistent(t *testing.T) {
	def := realDefs(t)["markdown"]
	if def == nil {
		t.Fatal("markdown def 未加载")
	}
	lines := []string{"```ts", t1Line159, "```", "", "after text"}

	// 入口1：HighlightStates（statesOnly）
	b1 := newTestLineStates(lines)
	NewHighlighter(def).HighlightStates(b1)
	if b1.states[2] != nil {
		t.Error("HighlightStates: 闭 fence 行 state 应为 nil")
	}
	if b1.states[4] != nil {
		t.Error("HighlightStates: 末行 state 应为 nil")
	}

	// 入口2：HighlightStates 定 state + HighlightMatches 定颜色
	b2 := newTestLineStates(lines)
	NewHighlighter(def).HighlightStates(b2)
	NewHighlighter(def).HighlightMatches(b2, 0, len(lines)-1)
	if b2.states[2] != nil {
		t.Error("HighlightMatches: 闭 fence 行 state 应为 nil")
	}

	// 入口3：HighlightString（单 highlighter 连续跑）
	strMatches := NewHighlighter(def).HighlightString(strings.Join(lines, "\n"))

	for i := range lines {
		if !reflect.DeepEqual(b2.matches[i], strMatches[i]) {
			t.Errorf("L%d 两入口 Match 不一致:\n  states+matches: %v\n  HighlightString: %v", i, b2.matches[i], strMatches[i])
		}
	}
}
