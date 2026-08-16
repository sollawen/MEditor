# R0：fence 内落单引号 state 粘滞导致 codeblock 吞段 — 修复实施方案

## 1. 问题与目标

**症状**：打开 `gameBuddy/gamePack/texas/docs/T1-tts播放完成后再上行方案.md`，从某个 codeblock 中间一行起直到文件尾，全部内容变白、MD 格式全失（标题、列表、代码块边框消失）。分界行是块内代码 `const txt = String(obj.chatTxt ?? '').replace(/["}']/g, ...)`。

**目标**：
1. 该文档的 codeblock 在下一条 fence 处正常闭合，其后段落各自恢复 MD 结构与渲染。
2. 一般化：任何「fence 内落单引号」的 md 文档不再吞段、不再大面积白屏。
3. 顺带修复因此暴露的既有测试失败（见 2.4）。

**关联文档**：
- 附录B：`docs/fence内落单引号state粘滞导致codeblock吞段-修复计划-附录B-P1颜色验证.md`（颜色层实验）
- `docs/多行结构紧跟codeblock的segment乱序修复.md`（issue #6，本次重构必须保持其行为）

## 2. 根因分析（已实验验证）

### 2.1 因果链

四环相扣，每环均已对照源码确认：

1. **落单引号开启跨行字符串 region**。分界行字符类 `["}']` 中的 `"` 是全文件唯一的双引号。markdown.yaml 的 ts fence 规则 `include: "typescript"` 嵌入 typescript.yaml 的 `constant.string` region（`start: "\""` / `end: "\""`，无行锚定、跨行延续）。`'` 成对未触发，`"` 落单触发。
2. **state 粘滞**。`pkg/highlight/highlighter.go` 的 `highlightRegion` 在行内找不到闭合引号时保持 `lastRegion` 不变（这是支持多行字符串的合法特性）。字符串 region 未闭合 ⇒ 外层 fence region 的 end 永远轮不到匹配 ⇒ state 非 nil 一路粘滞到文件尾。
3. **DetectSegments 误判**。`internal/md/detect.go` 用 `buf.State(y)` 的 nil/非 nil 转折判定 codeblock 边界。state 从 fence 行起永不归 nil ⇒ 该块被判「未闭合」，兜底分支把 `BufEndLine` 扩到 `visibleEnd` ⇒ 之后的标题/fence/列表/正文全部被吞进一个巨型 codeblock。
4. **渲染退化**。被吞内容全走 `RenderCodeBlock` 统一样式 ⇒ 全白无格式。

### 2.2 实验验证

独立实验程序（`/tmp/mdhl`，独立 module + replace 指向 microNeo 源码，未改仓库），加载真实 yaml 用 `pkg/highlight` 引擎跑。

**实验一（detect 层）**：

| 实验 | 改动 | 结果 |
|---|---|---|
| 原文件 | 无 | codeblock 从 159 行延伸到文件尾 263 行，state 永不归 nil |
| fixed.md | 250 行加一对 `"类型"` | 无效：第一个 `"` 闭合旧 region，第二个又开新 region |
| fixed3.md | 235 行加单个 `"` | 159 块在下一个 fence 处恢复闭合，后续段落全部恢复 |

结论：污染 = fence 内落单引号 + 后文直到文件尾无同类引号；下一个同类引号出现即自愈。fence 行内容是 markdown 的硬边界，用字符串匹配判定边界可完全绕开 state，根治吞段。

**实验二（颜色层，附录B）**：

- 污染行的 buffer Match 里只有整行一个 `constant.string`，无任何 `md-*` 组（state 卡在字符串 region 后，`highlightRegion` 只找闭合引号，永不回落顶层 md 规则）。
- fresh 单行 highlight 对同一行给出完全正确的 `md-*` 组（markdown.yaml 全部 `md-*` 规则都是 `^`/`$` 锚定的单行 regex，不依赖跨行 state；唯一跨行构造是 fence region，而 fence 行属于 codeblock 段，不走此路径）。
- 因此纯「过滤污染组」方案不可取：污染行过滤后无色可显，回落白色——结构对了颜色仍白。正确方案是污染检测 + 按需 fresh 重高亮。

### 2.3 影响面

- 158 个 yaml 语法文件中约 79 个定义双引号跨行字符串 region（grep 实测），66 个单引号、11 个反引号。
- markdown.yaml include 的全部语言（ts/js/python/go/c/java/ruby/sh 等）全部中招。
- 高频雷区：正则字符类、代码示例不完整、sh 块里英文撇号（it's）、模板字符串示例。

### 2.4 既有测试基线（与本 bug 无关，但界定验收口径）

`go test ./internal/md/` 当前 8 FAIL，分三类：

| 测试 | 根因 | 本计划 |
|---|---|---|
| TestDetectCodeBlock / WithTilde / WithPipe / UnclosedCodeBlock | detect 改 state 驱动后 mock `State()` 恒 nil，codeblock 检不出（commit e3c88f9d 后测试未更新） | P0 改回字符串驱动后自动恢复 |
| TestDetectHR | `isHR` 只认 `-`/`=`，缺 `*`/`_` | 顺带修复（3.4-2） |
| TestDetectBlockquoteWithEmptyLines / TestDetectMixedContent | blockquote 遇空行即闭合 vs 测试期望空行入块，语义决策未定 | 不在范围 |
| TestRenderTable_Simple（render_table_test.go） | 渲染层 separator 行 BufLine 语义，与 detect 无关 | 不在范围 |

**验收口径**：前 5 个恢复 PASS；后 3 个保持现状 FAIL，不新增恶化。

## 3. 修复设计

### 3.1 总体思路

分两层，各自独立可验：

- **P0（结构层）**：detect 的 codeblock 边界从「state 转折」改为「fence 行字符串匹配」。fence 是 markdown 硬边界，天然免疫嵌入语言 state 粘滞。修好后段落结构恢复，吞段消失——用户可感知的主症状由此解决。
- **P1（颜色层）**：state 粘滞仍存在于 buffer，污染行之后正文行的 buffer Match 是错的（整行 `constant.string`）。非 codeblock 段的 highlight overlay 增加「污染检测 + fresh 单行重高亮」，修好后颜色恢复。
- **P3（顺带）**：`isClosingFence` 补 `~~~`、`isHR` 补 `*`/`_`——都是 P0 扩大检出能力后的直接配套。

依赖关系：P1 依赖 P0 的 `Segment.IsCodeBlock` 标记；P3-1 依赖 P0 让 tilde 块可检出。

### 3.2 P0：detect 改 fence 行驱动

#### 3.2.1 新增 `isFenceLine`

```go
// isFenceLine 判断是否 fence 行：≥3 个反引号或波浪线开头。
// 对齐 markdown.yaml 的 fence regex ^ {0,3}(`{3,}|~{3,})；入参是已 TrimSpace 的行。
// 只看前缀不区分开/闭：开（```ts）与闭（```）由主循环 codeblockStart 状态区分。
func isFenceLine(trimmed string) bool {
	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}
```

从宽处理缩进（TrimSpace 而非限 3 空格）：差异仅影响 >3 缩进的 fence 行（极罕见），不产生错误高亮，仅 detect 边界略宽，见 §6。

#### 3.2.2 主循环重构（DetectSegments）

```go
codeblockStart := findOpenFenceBefore(buf, visibleStart) // 回溯初始化，块外为 -1

for y := visibleStart; y <= visibleEnd; y++ {
	if y >= buf.LinesNum() { // 越界保护（同现状）
		break
	}
	trimmed := strings.TrimSpace(string(buf.LineBytes(y)))

	if isFenceLine(trimmed) {
		if codeblockStart == -1 {
			closeOpenStructures(&segments, &state, startLine, y) // 先关闭未闭合多行结构
			codeblockStart = y
		} else {
			// 闭 fence：emit codeblock 段（含两侧行）
			segments = append(segments, Segment{
				BufStartLine: codeblockStart, BufEndLine: y,
				Render: RenderCodeBlock, IsCodeBlock: true,
			})
			codeblockStart = -1
		}
		continue // fence 行已归入 codeblock，不做字符串匹配
	}
	if codeblockStart != -1 {
		continue // 块内行：归入 codeblock，不产生独立 segment
	}
	// ↓ 与现状完全相同的字符串匹配（heading/table/list/blockquote/HR/normal）
}

// 兜底（同现状）：块未闭合 → emit [codeblockStart, visibleEnd]，IsCodeBlock: true
// 兜底（同现状）：关闭剩余未闭合的多行结构
```

与现状的关键差异只有两处：codeblock 边界改由 fence 行内容翻转 `codeblockStart`；不再读写 `buf.State()`。字符串匹配部分（含 issue #6 之外的全部状态机逻辑）原样保留。

`closeOpenStructures` 把现有「进入 codeblock 前关闭未闭合多行结构」的 switch 块（issue #6 修复，见 detect.go 进入 codeblock 分支）抽成函数，仅供主循环开 fence 分支调用，行为不变。注意收尾兜底的 switch **不复用**它——兜底 emit 的是 `[startLine, visibleEnd]` 且循环已结束无需归位 state，语义不同，保持原状：

```go
// closeOpenStructures 在进入 codeblock 前关闭未闭合的多行结构，
// 否则它们会被 codeblock 吞掉导致 segment 顺序倒挂（issue #6）。
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
```

#### 3.2.3 新增 `findOpenFenceBefore`（回溯初始化）

现状用 `buf.State(visibleStart-1)` 初始化 `lastState`；改为从 buffer 头扫到 `visibleStart-1` 数 fence 行奇偶：

```go
// findOpenFenceBefore 返回 visibleStart 所在未闭合 codeblock 的开 fence 行号，块外返回 -1。
// 从第 0 行扫到 visibleStart-1，fence 行奇数次出现后在块内（记住行号），偶数次后回块外。
// 生产热路径恒 visibleStart==0（buffer.go 全量调用），本函数零开销；
// 若未来 detect 改增量、visibleStart>0 进入热路径，需把结果缓存到 SharedBuffer。
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
```

回溯与主循环用同一套 `isFenceLine` 规则，结果天然一致；`visibleStart==0` 时循环不执行。当前生产路径 buffer.go 恒以 `(0, LinesNum()-1)` 全量调用 DetectSegments（buffer.go:218、1060），本函数在生产路径零开销；仅测试（TestDetectCodeBlockMidStart）会传非零 visibleStart。

#### 3.2.4 `State()` 从 BufferReader 接口退役

- 删除 `BufferReader.State(n int) highlight.State`：md 包内只有 detect.go 用它（两处），P0 后无任何调用方；`*buffer.Buffer` 结构化满足收窄后的接口，无需改动。
- detect.go 与 detect_test.go 的 `pkg/highlight` import 一并移除；mockBuffer 删掉 `State()` 方法。render_table_test.go 的 `mockBufferForTest.State()`（该文件唯一的 highlight 引用）同理可删——接口收窄后多余方法仍合法编译，不删亦无害；删掉则该文件可一并去掉 highlight import。
- `detectState`（blockquote/table/list 状态机）与本次无关，保留。

### 3.3 P1：非 codeblock 段污染检测 + fresh 重高亮

#### 3.3.1 约束：普通 MD 文本的颜色全部来自 highlight overlay

`renderInline` 只设文本属性（bold/italic/underline），颜色全靠 `expandLineStyles`（bufwindow_md.go）叠加 `md-header` / `md-list` / `md-blockquote` / `md-link` / `md-inline-code` / `md-bold` / `md-hr` 等组。因此**不能整体去掉非 codeblock 段的 overlay**（否则 MD 配色全清空），也不能只做「过滤污染组」（附录B已证：污染行过滤后无色可显）。方案是污染检测 + 按需 fresh。

#### 3.3.2 `Segment` 增加字段

`internal/md/md.go`：

```go
IsCodeBlock bool // detect 层标记：该段是否 codeblock（render 层决定是否走污染检测）
```

detect.go 两处 codeblock emit（闭 fence 处、兜底未闭合处）设 true，其余默认 false。

#### 3.3.3 `expandLineStyles` 改造

签名加 `isCodeBlock`（调用点 renderSegmentMD 内透传 `seg.IsCodeBlock`）：

```go
func (w *BufWindow) expandLineStyles(bufLine int, runeCount int, baseStyle tcell.Style, isCodeBlock bool) []tcell.Style {
	match := w.Buf.Match(bufLine)
	if !isCodeBlock && polluted(match) {
		match = freshLineMatch(w.Buf.SyntaxDef, string(w.Buf.LineBytes(bufLine)))
	}
	// ↓ 展开循环不变（现状逻辑）
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
```

codeblock 段**必须跳过**污染检测：块内 Match 本来就是嵌入语言组（`constant.string` 等），`polluted` 会恒真，跳过才能让嵌入语言高亮正常工作（块内残留污染是已接受的 P2 限制，见 §4）。

干净行误报为零：干净 md 文档的行 Match 只含 `md-*` 组与空组，`polluted` 恒 false，行为与现状逐字节一致。

#### 3.3.4 `freshLineMatch` 用一次性 Highlighter

```go
// freshLineMatch 对单行做无状态重高亮，绕开 buffer 里粘滞的跨行 region state。
// 不挂 BufWindow，便于独立单测。
func freshLineMatch(def *highlight.Def, line string) highlight.LineMatch
```

实现：每次调用 `highlight.NewHighlighter(w.Buf.SyntaxDef)` 新建**一次性 Highlighter** 再 `HighlightString`。**禁止复用 buffer 共享的 `w.Buf.Highlighter`**：那是可变对象，`HighlightString` 会读写 `h.lastRegion`，显示线程与 buffer 编辑线程共享会引入竞态，且结果依赖进入时的 lastRegion。一次性 Highlighter 很轻（Def 指针 + lastRegion 两个字段），单行输入从 nil 态开始，结果确定且正确（附录B已验证 md-* 规则全部单行可重算）。

#### 3.3.5 性能量化

- fresh 只在污染行触发，上限 = 可见行数（一屏约 50 行）。
- markdown 顶层约 15 个 pattern + 约 30 个 fence region start regex ≈ 45 次短行 regex/行；污染满屏约 2000+ 次/帧，微秒~低毫秒级，且仅打开被污染文档时发生。
- 干净文档零开销。
- **不做**「fresh 结果写回 `b.SetMatch`」缓存：会覆盖 state 派生的真实 Match、污染 buffer 全局状态，得不偿失。

### 3.4 P3：顺带修复

1. **`isClosingFence` 补 `~~~`**（render_codeblock.go）。现状只认三反引号；P0 让 tilde 块可检出后不补会导致 tilde 块「上边框有、下边框无」。改为与 `isFenceLine` 同源：

```go
func isClosingFence(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}
```

2. **`isHR` 补 `*`/`_`**（detect.go）。markdown.yaml 的 hr 规则 `^(---+|===+|___+|\*\*\*+)\s*$` 与 TestDetectHR 均要求四种字符；现状只认 `-`/`=`：

```go
// isHR 判断水平分割线：- = * _ 任一字符重复 ≥3 次。入参主循环已 TrimSpace。
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
```

无冲突：`___` 不匹配列表前缀（`* ` 需空格），`***` 同理。

## 4. 明确不做

| 项 | 理由 |
|---|---|
| 改 highlight 引擎 | 多行字符串 region 是合法特性，纯 .ts 文件打开也依赖它 |
| 批量改 80 个语言 yaml | 无法穷举落单引号场景；markdown.yaml 的 fence end 规则在字符串 region 内本来就不会被检查，改 yaml 治不了本 |
| codeblock 内残留污染（P2） | 落单引号所在的那一个块内部仍会从该行染字符串色到块尾；fence 行/边框色同样受染（state 粘滞时 fence 行的 Match 也是 constant.string，isCodeBlock=true 跳过检测后不恢复）。与 micro 原生打开 .ts 行为一致，接受，验收时视为已知现象非新 bug；彻底方案（每块独立子 highlighter）成本高 |
| `Group.String()` O(N) 反向索引优化 | 属普适性能改进，与本计划解耦；污染检测每行仅调几次 String()，量级可忽略，另行处理 |

## 5. 测试方案

### 5.1 detect 单测（internal/md/detect_test.go）

mockBuffer 删除 `State()` 方法（接口已删），移除 highlight import。新增用例：

| 用例 | 内容 | 锁定 |
|---|---|---|
| TestDetectCodeBlockWithLoneQuote | `ts fence` 内放落单双引号行（`/["}']/g`）+ 闭 fence + 后续 `### heading` 与第二个 fence | **P0 核心回归**（对应 T1 症状与 fixed3 实验）：块在下一条 fence 闭合、后续段落独立、不吞到 EOF；同时断言 codeblock 段 `IsCodeBlock==true`、heading 段为 false（锁 P0→P1 门控契约） |
| TestDetectCodeBlockMidStart | `DetectSegments(buf, 2, 3)`，visibleStart 落在块中间 | 回溯初始化正确 |
| TestDetectListBeforeFence | 未闭合 list 后紧跟 fence | list 在 y-1 关闭、无 segment 乱序（issue #6 行为显式锁定，此前无专门用例） |
| TestDetectFenceIndented | 带 1~3 空格缩进的 fence 行 | TrimSpace 从宽处理 |

既有用例恢复：4 个 codeblock 测试（P0 自动恢复）+ TestDetectHR（3.4-2 恢复）。

### 5.2 P1 单测（internal/display）

P1 是颜色层修复的核心，没有用例则改完无法验证。新增：

| 用例 | 内容 |
|---|---|
| TestPolluted | `{0: md-header}` 不污染；`{0: constant.string}` 污染；空组名中性 |
| TestFreshLineMatch | 用真实 markdown.yaml 的 Def 对 `### heading`、`- item` 单行 fresh，断言命中 md-header / md-list 组（freshLineMatch 不挂 BufWindow，可直接单测） |

### 5.3 手工验收

1. T1 文档（含双引号 + 模板字符串反引号两类污染）：159 块正常闭合，后续标题/列表/fence 恢复 MD 结构与配色。
2. 干净文档回归（`docs/sample.md` 等）：渲染逐像素不变。
3. 编辑模式进出 codeblock、回退原生渲染不受影响。
4. `make build` 通过；`go test ./internal/md/ ./internal/display/`：5 个恢复 + 新增全绿，3 个既有 FAIL 维持现状。

## 6. 风险与边界

| 风险 | 评估 |
|---|---|
| fence 字符串判定与 yaml fence region 语义偏差 | yaml 缩进限 ≤3 空格，helper 从宽（TrimSpace）。差异仅影响 >3 缩进 fence 行（极罕见），不产生错误高亮，仅 detect 边界略宽 |
| 代码行以三反引号开头但非 fence 的误判 | markdown 语义内不存在：块内就是闭 fence、块外就是开 fence |
| 闭 fence 长度 < 开 fence、嵌套 fence（块内出现三反引号行）、混合字符闭合（反引号开波浪线闭） | `isFenceLine` 只看前缀、toggle 语义，会提前闭合。markdown 规范要求同字符且闭 ≥ 开，但 markdown.yaml 的 fence end regex 同样是任一 fence 行终结 region、不校验长度/字符——detect 与高亮引擎行为一致，非新引入不一致。极罕见，接受为已知边界 |
| findOpenFenceBefore 每帧 O(visibleStart) | 生产路径恒 visibleStart==0 零开销；未来 detect 改增量且大文件滚动有感知时再缓存到 SharedBuffer（函数注释已留 hook） |
| expandLineStyles 行为变化 | 仅「是否用 buffer Match」一点：非 codeblock 段污染行换成 fresh Match，其余行与现状逐字节一致；MD 格式（bold/inline-code/link）不丢 |
| editMode / renderSegmentNative 路径 | 不改。renderSegmentNative 按 segment 行范围原生渲染、不做 MD 分类，detect 只改变行的段归属（fence 内行跳过字符串匹配不影响其渲染），不受影响 |

## 7. 实施步骤

按依赖排序，每步可独立验证：

| # | 改动 | 验证 |
|---|---|---|
| 1 | `internal/md/md.go`：`Segment` 加 `IsCodeBlock bool` | 编译过 |
| 2 | `internal/md/detect.go`：新增 `isFenceLine` / `findOpenFenceBefore` / `closeOpenStructures`；主循环改 fence 驱动；两处 codeblock emit 设 `IsCodeBlock: true`；删 `BufferReader.State()` 与 highlight import；`isHR` 补 `*`/`_` | `go test ./internal/md/`：4 个 codeblock 测试 + TestDetectHR 恢复 PASS（接口收窄不影响 mock——实现方多余方法合法，其清理在步 4） |
| 3 | `internal/md/render_codeblock.go`：`isClosingFence` 补 `~~~` | 手开 tilde 块样例：上下边框齐全 |
| 4 | `internal/md/detect_test.go`：删 mock State 方法；补 5.1 四个新用例 | 新用例全 PASS |
| 5 | `internal/display/bufwindow_md.go`：`expandLineStyles` 加 `isCodeBlock` 参数（调用点透传 `seg.IsCodeBlock`）+ `polluted` + `freshLineMatch`（一次性 Highlighter，见 3.3.4） | 编译过；T1 文档污染尾部颜色恢复 |
| 6 | `internal/display` P1 单测：`TestPolluted` / `TestFreshLineMatch` | 全 PASS |
| 7 | `make build` + 5.3 手工验收全项 | 全过 |

预计改动量：detect.go 约 ±60 行（净简化）、bufwindow_md.go 约 +40 行、测试 +150 行，其余为个位数行级改动。
