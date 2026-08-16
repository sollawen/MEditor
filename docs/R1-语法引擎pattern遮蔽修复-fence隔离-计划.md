# R1：语法引擎 pattern 遮蔽修复 + fence 状态隔离 — 实施计划

## 1. 问题与目标

**症状（v1.1.26 / R0 落地后的残留）**：T1 文档 159 行 `const txt = ...replace(/["}']/g, ...)` 起，其后**所有** codeblock 内部文字全部近白、无语法高亮；159 行之前的块正常；块外正文正常（P1 救回）。

**对照基线**：VSCode 打开同一文档完全正确——包括 159 行所在的块内部、以及其后所有块。

**目标**：

1. T1 类污染（引号被正则字面量/字符类包含）从**源头**消除：引擎不再把 pattern 匹配 extent 内部的引号当作 region start。
2. 任何残留污染（真·未闭合字符串）不再跨 fence 扩散：codeblock 段按块独立高亮。
3. 全量语法回归通过（158 个 yaml 不出现预期外 diff）。

**前置结论（本轮排查已确认）**：

- 引擎与 markdown.yaml 均为 upstream micro 原样继承，原版 micro 同样复现此 bug（无 MD 渲染层，表现为「后面全部一色」）。
- R0 的 P0（fence 驱动 detect）、P3（tilde/HR）保留不动；R1 落地验收后，由 R2 退役 P1（`polluted()` / `freshLineMatch()` 那套启发式）。

**关联文档**：
- `docs/R0-codeblock吞段-计划.md`（前作，P0/P1/P3 的来龙去脉）
- `docs/R2-退役P1污染检测-计划.md`（后续清理，依赖本计划落地）

## 2. 根因分析（已实验验证）

### 2.1 引擎优先级缺陷：region start 无条件碾压 pattern

实验程序（`/tmp/mdhl`，加载真实 runtime/syntax yaml 跑 `HighlightStates` + `HighlightMatches`）对 159 行逐 rune 输出 Match 组：

```
col 38  '  → constant.string   （'' 配对，正常开闭）
col 50  "  → constant.string   （落单，开启 region，state 粘滞）
col 51  }  → constant.string   （染到行尾）
```

关键事实：**整行没有任何正则字面量组**——尽管 `typescript.yaml` 第 21 行就有正则规则：

```yaml
- constant: "/[^*]([^/]|(\\\\/))*[^\\\\]/[gim]*"
```

实测该规则匹配 159 行的 `/["}']/g` 于列 [48, 56]，引号（列 50）**严格落在匹配 extent 内部**。这条规则就是为正则字面量准备的——VSCode 语义下整个 `/["}']/g` 是一个 token，里面的 `"` 只是字符类成员，根本不是字符串定界符。

但引擎从未让它生效。`pkg/highlight/highlighter.go` 的 `highlightRegion`（L113）与 `highlightEmptyRegion`（L207）都是同一结构：先扫描嵌套 region 的 start，**只要行内任何位置找到 start（列 50 的 `"`），立刻递归进 region 并 return，pattern 从头到尾没被尝试**。

### 2.2 与 upstream 修复脉络的关系

引擎 2023 年 upstream 提交 `ceaa143c`（#2840 "Fix regions and patterns inside regions"）已把「region end vs 嵌套 region start」改成按位置最左优先（`firstLoc` 竞争逻辑即那次引入），但**漏了「pattern vs region start」这最后一组竞争关系**。本修复是它的自然续章：pattern 与 region start 同样按位置竞争，且 pattern 的匹配 extent 消费掉其内部的 region start。

### 2.3 残留类：真·未闭合字符串（引擎修复不覆盖）

文档代码示例里写了个没写完的 `"abc`，或 sh 块里的英文撇号（`it's`）——这类引号**不被任何 pattern 遮蔽**，开 region 是字符串语法的合法行为（VSCode 在真实 .ts 里同样绿到下一个引号）。此时 state 依然粘滞，闭 fence 行在 string region 内不被 fence end 检查 → 污染照样跨 fence 扩散到后续所有块。

VSCode 的 markdown 之所以不扩散，是因为**每个 fence 是独立 scope，语法状态不跨块**。对应到 microNeo，就是本计划 B 部分：codeblock 段按块 fresh 高亮。

### 2.4 两类污染与两层修复的对应关系

| 污染类别 | 例子 | 引擎修复（A） | 块级 fresh（B） |
|---|---|---|---|
| 引号被 pattern 遮蔽 | T1:159（regex 字符类）、字符类含引号的各类示例 | ✓ 根治：region 压根不开 | ✓ 同样正确（双保险） |
| 真·未闭合字符串 | `"abc` 没写完、sh 块 `it's` | ✗ region 合法开启（与 VSCode 一致） | ✓ 隔离：最多染到所在块尾，不跨 fence |

两类合起来：markdown 文档中**不再存在跨 fence 的 state 污染**。这是 R2 能安全退役 P1 的结构前提。

## 3. 修复设计

### 3.1 A：引擎 — pattern extent 遮蔽 region start

**语义定义**：设某行内嵌套 region start 匹配于位置 X。若存在一条「适用的 pattern」匹配 [s, e] 满足 `s < X < e`（X 严格在 extent 内部），则该 region start **不成立**：视同本行无嵌套 start，落入既有的 pattern 全行涂色路径，state 不进入该 region。

设计要点：

1. **同位置不遮蔽**（`s == X` 时 region 照常赢）：保守取舍，保持所有「pattern 与 region 同起点」场景的现有行为零变化，把回归面压到最小。TextMate 按规则书写顺序裁决同位置，但 micro 的 yaml 解析把 regions / patterns 拆进两个列表、原始交错顺序已丢失；保留顺序需要动 parser 结构与 `ResolveIncludes`，收益仅限罕见同位置冲突，不值得。
2. **只遮蔽嵌套 start，不遮蔽 end**：字符串内 pattern（受 limitGroup 门控）遮蔽 region end 是更大的语义变更，且现有 yaml 的 `skip: "\\\\."` 已覆盖最常见的转义场景。明确不做，见 §4。
3. **statesOnly 两种模式行为必须一致**：遮蔽检查不放进任何 `if !statesOnly` 块——region 开启决策同时影响 `h.lastRegion`（state）与 highlights（match），`HighlightStates` 与 `HighlightMatches` 必须看到相同决策，否则 state 与颜色错位。
4. **适用的 pattern 集合**：与既有 pattern 涂色路径用同一套门控——`highlightRegion` 内是 limitGroup 门控（`curRegion.group == curRegion.limitGroup || p.group == curRegion.limitGroup`），`highlightEmptyRegion` 内是全部 `h.Def.rules.patterns`。不新设门控规则。

**改动点（两个函数各插一段，共约 ±25 行）**：

`highlightRegion`，L145 的 `if firstRegion != nil && firstLoc[0] != lineLen` 之前：

```go
// pattern 匹配 extent 内部的 region start 不成立：更早开始的 pattern 吃掉了它
if firstRegion != nil && shielded(curRegion.rules, line, firstLoc[0], curRegion) {
    firstRegion = nil
}
```

`highlightEmptyRegion`，L227 同理（门控换为顶层全量）。

新增 helper（放 highlighter.go）：

```go
// shielded 判断 pos 是否落在某条适用 pattern 的匹配 extent 内部：
// region start 出现在 pattern 匹配范围内时不成立（最左优先，pattern 消费其 extent）。
// 同位置（s == pos）不遮蔽，保持既有行为。
func shielded(rs *rules, line []byte, pos int, cur *region) bool {
    for _, p := range rs.patterns {
        if cur != nil {
            if cur.group != cur.limitGroup && p.group != cur.limitGroup {
                continue
            }
        }
        for _, m := range findAllIndex(p.regex, line) {
            if m[0] < pos && pos < m[1] {
                return true
            }
        }
    }
    return false
}
```

（顶层调用传 `cur == nil` 表示无门控；具体签名实现时可微调，语义不变。）

**遮蔽后的自然路径（不写新代码，走既有分支）**：`firstRegion = nil` → 落到既有的 `fullHighlights` pattern 全行涂色（其 `endLoc` 门控照旧）→ 行尾 `loc == nil` → `h.lastRegion = curRegion`（保持 fence region 内）→ 下一行继续在 fence 内正常高亮。以 T1:159 为例：整行按 ts pattern 涂色（正则规则命中 [48,56]），state 留在 fence region，**后续块内每行 Match 恢复正常语法组**——这正是 VSCode 的行为。

### 3.2 B：fence 隔离 — codeblock 段按块 fresh 高亮

**语义定义**：codeblock 段的颜色不再取 buffer Match（受全局 state 污染），改为对该段行范围跑一次性子高亮，从 nil state 开始。块内任何污染（含真·未闭合字符串）最多染到块尾，构造上不可能跨 fence。

markdown.yaml 的 fence region 覆盖完备（每个具名语言一个 region + `other (default)` 兜底 + `md-codeblock` 未知语言 fallback），从块首行（开 fence）开始 fresh 会自然进入正确的嵌入语言 region，机制上与 buffer 全量高亮同源，只是起点为 nil state。

**改动点（internal/display/bufwindow_md.go）**：

1. 从 `expandLineStyles` 抽出纯函数 `expandMatch(match, runeCount, baseStyle) []tcell.Style`（把「稀疏 Match → 稠密 style」的展开循环移进去，无行为变化）。
2. 新增：

```go
// freshBlockMatches 对 codeblock 段做整块独立高亮（一次性 Highlighter，nil state 起步）。
// fence 是 markdown 硬边界，块内残留污染（如未闭合字符串）最多染到块尾，不跨块。
func freshBlockMatches(def *highlight.Def, lines []string) []highlight.LineMatch {
    if def == nil {
        return nil
    }
    h := highlight.NewHighlighter(def)
    return h.HighlightString(strings.Join(lines, "\n"))
}
```

（沿用 P1 已验证的一次性 Highlighter 纪律：不碰 buffer 共享的 `w.Buf.Highlighter`，无竞态。）

3. `renderSegmentMD` 的 lineStyles 预计算分叉：

```go
if seg.IsCodeBlock {
    blockMatches := freshBlockMatches(w.Buf.SyntaxDef, blockLines(seg))
    // 按 seg 内相对行号取 match，expandMatch 展开
} else {
    // 现状路径：w.Buf.Match + P1 污染检测（R1 不动，R2 退役）
}
```

`Segment.IsCodeBlock`（P0 引入）直接复用，不加新字段。

**性能**：每帧每可见 codeblock 一次，O(块行数 × pattern 数)，与 R0 §3.3.5 对满屏 fresh 的估算同级（µs~低 ms）。块通常几十行。先不做缓存；若实测滚动有感知，再按（块行范围 + 首行内容）加 per-frame 缓存，函数注释留 hook。

**边界**：

- 未闭合块（无闭 fence）：detect 兜底段 `[codeblockStart, visibleEnd]`，同样走块级 fresh，污染封在段尾，无后续行可染。
- editMode / `renderSegmentNative`：不改。编辑模式按 buffer 原生渲染，残留 micro 原版行为（与用户实测原版 micro 一致），属已接受现状。
- 块级 fresh 与引擎修复（A）对 T1 类双保险：两条路径各自独立正确，不依赖对方存在。

### 3.3 与 R0 各层的关系

| 层 | 来源 | R1 动作 |
|---|---|---|
| P0 fence 驱动 detect | v1.1.26 | 保留。真·未闭合字符串时 detect 结构仍需它兜底（state 驱动会吞段） |
| P1 污染检测 + 单行 fresh | v1.1.26 | 保留不动，R1 验收后由 R2 退役 |
| P3 tilde/HR | v1.1.26 | 保留 |
| 引擎 highlighter.go | upstream | **本计划 A 首次修改**（upstream #2840 续章） |
| codeblock 颜色来源 | buffer Match | **本计划 B 改为块级 fresh** |

## 4. 明确不做

| 项 | 理由 |
|---|---|
| pattern 遮蔽 region **end** | 字符串内 pattern 受 limitGroup 门控，遮蔽 end 影响面是另一族语义；`skip` 已覆盖转义场景。如日后有真实案例再立项 |
| 保留 yaml 规则原始顺序（同位置 TextMate 裁决） | 需改 parser 结构 + ResolveIncludes，收益仅限罕见同位置冲突；同位置保守让 region 赢，回归面最小 |
| 改 158 个 yaml | 规则就在 yaml 里（typescript.yaml:21），缺的是引擎优先级，不是规则 |
| 块级 fresh 结果写回 buffer（SetMatch） | 覆盖 state 派生的真实 Match、污染 buffer 全局状态；显示层自用即可 |
| R2 的清理 | 单独计划（`docs/R2-退役P1污染检测-计划.md`），门禁：本计划验收通过 |
| revert v1.1.26 | P0/P3 本来就对；P1 前向删除（R2），不回头 revert |

## 5. 测试方案

### 5.1 引擎单测（pkg/highlight/highlighter_test.go，新建；包内目前无测试文件）

从 `../../runtime/syntax` 加载真实 yaml（复用 buffer.go 的加载 + ResolveIncludes 逻辑，测试内小工具函数实现）。

| 用例 | 内容 | 断言 |
|---|---|---|
| TestPatternShieldsRegionStart | ts Def，单行 `const x = /["}']/g` | Match 含正则 constant 组；**无**整行 constant.string；行末 state 为 nil（顶层） |
| TestRegionStartUnshielded | ts Def，单行 `const s = "abc`（无 pattern 遮蔽） | string region 照常开启（state 非 nil）——真未闭合字符串行为不回退 |
| TestShieldInsideFence | md Def，三行：```` ```ts ```` / T1:159 原文 / ` ``` ` | 块内行 Match 为正常 ts 组；闭 fence 行后 state 归 nil；正则组出现 |
| TestShieldStatesMatchesConsistent | 同上输入，`HighlightStates` + `HighlightMatches` 各跑一遍 | 闭 fence 后一行 state 为 nil（两套入口决策一致） |

### 5.2 块级 fresh 单测（internal/display/bufwindow_md_test.go 新增）

| 用例 | 内容 | 断言 |
|---|---|---|
| TestFreshBlockMatches | md Def，ts fence 块（含 T1:159 行） | 块内行有正常语法组，非整行 constant.string |
| TestFreshBlockIsolation | 两个 ts fence 块，第一块内放真未闭合 `"abc` | 第一块尾部行染 string（合理残留，同 VSCode）；**第二块行完全干净**（隔离性核心断言） |

### 5.3 全量语法回归 harness（临时实验程序，同 R0 /tmp/mdhl 做法，不入库）

1. 基线：当前 HEAD 构建二进制，对语料逐文件跑 `HighlightStates` + `HighlightMatches`，逐行输出组名摘要落盘。
2. 改后构建重跑，diff 两份输出。
3. 语料：本仓库 Go 源（internal/ pkg/ 抽样）、runtime/syntax/*.yaml 全部、docs/*.md + T1 文档、gameBuddy 下真实 .ts 源码。
4. **预期 diff 白名单**：仅「字符串/注释 region start 被更早 pattern 遮蔽」类（T1:159 为代表：整行 string → 正常组）。出现任何其他类别 → 停下分析，不放行。

### 5.4 手工验收

1. T1 文档：159 行所在块**同块内部**恢复语法高亮（对齐 VSCode）；其后所有块恢复；块外正文不变。
2. 真·未闭合字符串样例（自造 md：sh 块内 `it's`）：该块尾部染 string 到块尾即止，下一块干净，块外结构完整。
3. 干净文档（docs/sample.md 等）逐像素不变。
4. 非 md 文件抽查（Go/yaml/ts 源码）：高亮与改前一致（回归 harness 已覆盖，肉眼二次确认）。
5. `make build`；`go test ./pkg/highlight/ ./internal/md/ ./internal/display/` 全绿（R0 遗留的 3 个既有 FAIL 维持现状不恶化）。

## 6. 风险与边界

| 风险 | 评估与对策 |
|---|---|
| 引擎改动影响全部 158 个 yaml | 回归 harness（5.3）逐行 diff + 白名单放行；改动本身只在「行内有 region start 且被 pattern extent 覆盖」时改变行为 |
| 同位置遮蔽语义与 TextMate 不完全一致 | 保守选择 region 赢；已知案例（T1 类）均为 pattern 更靠前，不依赖同位置裁决 |
| 遮蔽检查性能 | 仅在行内存在 region start 时触发，`findAllIndex` 与既有 pattern 涂色路径同源同级；limitGroup 门控下区域外 pattern 零成本跳过 |
| statesOnly 与非 statesOnly 决策漂移 | 设计铁律（3.1 要点 3）+ TestShieldStatesMatchesConsistent 锁定 |
| 块级 fresh 每帧成本 | 与 R0 §3.3.5 满屏 fresh 估算同级；先不缓存，实测有感知再加（注释留 hook） |
| 与 upstream micro 分歧 | 改动是 upstream #2840 的自然续章、修的是所有 micro 用户都有的 bug；保持外科手术式小改 + 充分测试，具备后续提 PR 回 upstream 的条件 |

## 7. 实施步骤

按依赖排序，每步可独立验证：

| # | 改动 | 验证 |
|---|---|---|
| 1 | 基线落盘：5.3 harness 用当前 HEAD 生成基线输出 | 基线文件生成 |
| 2 | `pkg/highlight/highlighter.go`：新增 `shielded`；`highlightRegion` L145 前、`highlightEmptyRegion` L227 前各插遮蔽检查 | 编译过；/tmp/mdhl 复跑 T1：159 行 Match 出现正则组、state 行进恢复正常 |
| 3 | `pkg/highlight/highlighter_test.go` 新建，5.1 四个用例 | 全 PASS |
| 4 | 5.3 harness 改后重跑 + diff | 仅白名单类别 diff；否则停下分析 |
| 5 | `internal/display/bufwindow_md.go`：抽 `expandMatch`；新增 `freshBlockMatches`；`renderSegmentMD` codeblock 分叉 | 编译过；T1 文档块内颜色恢复 |
| 6 | `internal/display` 5.2 两个用例 | 全 PASS |
| 7 | `make build` + 5.4 手工验收全项 | 全过 |

预计改动量：highlighter.go 约 ±30 行、bufwindow_md.go 约 +35/−5 行、新测试约 +120 行。

R1 验收发布后，执行 `docs/R2-退役P1污染检测-计划.md`。
