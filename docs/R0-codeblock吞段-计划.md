# fence 内落单引号 state 粘滞导致 codeblock 吞段 — 修复计划

## 1. 问题现象

打开 `gameBuddy/gamePack/texas/docs/T1-tts播放完成后再上行方案.md`，从某一行起（`const txt = String(obj.chatTxt ?? '').replace(/["}'']/g, ...)`）直到文件尾，全部内容变成白色、无任何 MD 格式：标题、列表、代码块边框全部消失。

## 2. 根因分析（已实验验证）

### 2.1 因果链

1. **落单引号开启字符串 region**：169 行正则字符类 `["}'']` 里的 `"` 是全文件唯一一个双引号（全文 `grep '"'` 仅 1 处命中）。markdown.yaml 的 ts fence 规则 `include: "typescript"` 嵌入了 typescript.yaml 的 `constant.string` region（`start: "\"" / end: "\""`，无行锚定、跨行延续）。`'` 是成对的没触发，`"` 落单触发了。
2. **state 粘滞**：`pkg/highlight/highlighter.go` 的 `highlightRegion` 在行内找不到闭合引号时保持 `lastRegion` 不变（这是支持多行字符串的合法特性）。字符串 region 未闭合 ⇒ 嵌套的外层 fence region 也永远轮不到 end 匹配 ⇒ state 非 nil 一路粘滞到文件尾。
3. **DetectSegments 误判**：`internal/md/detect.go` 用 `buf.State(y)` 的 nil/非 nil 转折判定 codeblock 边界。state 从 159 行（```ts 开 fence）起永不归 nil ⇒ 该 codeblock 被判"未闭合"，兜底分支把 `BufEndLine` 扩到 `visibleEnd` ⇒ 159 行之后所有标题/fence/列表/正文全部被吞进这一个巨型 codeblock。
4. **渲染退化**：被吞内容全部走 `RenderCodeBlock`，统一 `md-codeblock` 样式渲染 ⇒ 全白无格式。

### 2.2 实验记录

测试程序 `/tmp/mdhl`（独立 module，replace 指向 microNeo 源码，未改动仓库代码），加载真实 yaml 用 `pkg/highlight` 引擎跑逐行 state：

| 实验 | 改动 | 结果 |
|---|---|---|
| 原文件 | 无 | codeblock 从 159 行延伸到文件尾 263 行，state 永不归 nil |
| fixed.md | 250 行加一对 `"类型"` | 无效：第一个 `"` 闭合旧 region，第二个又开新 region |
| fixed3.md | 235 行加单个 `"` | 159 块在下一个 fence 处恢复闭合，后续段落全部恢复 |

结论：污染 = fence 内落单引号 + 后文直到文件尾无同类引号。一旦下一个同类引号出现即自愈，但整个文件只有一个引号时污染贯穿到尾。

### 2.3 影响面

- 158 个 yaml 语法文件中，约 79 个定义了双引号跨行字符串 region（grep `start: "\""` 实测 79），66 个定义了单引号，11 个定义了反引号。
- markdown.yaml include 的所有语言（ts/js/python/go/c/java/ruby/sh 等）全部中招。
- 高频雷区：正则字符类 `["}']`、代码示例不完整、sh 块里的英文撇号 `it's`、模板字符串示例。

### 2.4 现状附带问题：8 个测试本来就是挂的

`go test ./internal/md/` 当前 **8 个 FAIL**，分两类：

| 测试 | 根因 | 本计划能否修复 |
|---|---|---|
| TestDetectCodeBlock / WithTilde / WithPipe / UnclosedCodeBlock | detect 改 state 驱动后 mock `State()` 恒 nil，codeblock 检不出（commit e3c88f9d 后测试未更新） | ✅ P0 改回 fence 字符串驱动后自动恢复 |
| TestDetectBlockquoteWithEmptyLines / TestDetectMixedContent | blockquote 遇空行即闭合的语义与测试期望（空行入块）不一致 | ❌ 独立问题，不在本计划范围 |
| TestDetectHR | `isHR` 只支持 `-`/`=`，测试与 markdown.yaml 均要求 `*`/`_` | ⭕ 顺带修复（见 3.3） |
| TestRenderTable_Simple（render_table_test.go） | 渲染层 separator 行 BufLine 期望 -1 实际 1，与 detect 无关 | ❌ 独立问题，不在本计划范围 |

## 3. 修复方案

### P0：detect.go 改为 fence 行字符串驱动 codeblock 边界（核心修复）

**思路**：markdown 语义里 fence 行（``` / ~~~ 开头）本来就是 codeblock 的硬边界，优先级高于嵌入语言的任何语法状态。不再信任 state 的 nil/非 nil 转折判定 codeblock，直接用行内容匹配。

#### 3.1.1 新增 helper `isFenceLine`

```go
// isFenceLine 判断是否 fence 行（≥3 个 backtick 或 tilde 开头）。
// 对齐 markdown.yaml 的 fence regex：^ {0,3}(`{3,}|~{3,})。
// 入参是主循环里已 TrimSpace 的行；缩进 >3 的 fence 极罕见，从宽处理。
func isFenceLine(trimmed string) bool {
	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}
```

逻辑说明：只看前缀、不区分开/闭。开 fence（```ts）和闭 fence（```）都以 ```
开头，二者由主循环的状态区分——`codeblockStart == -1` 时是开，非 -1 时是闭。
 fence 后的 lang 标签、闭合 fence 的裸形态都不影响前缀判定。

注意与现有 `isClosingFence` 的关系：`isClosingFence`（render_codeblock.go 用于判断 segment 末行是否闭合 fence，决定是否画底边框）需同步补 `~~~` 识别，否则 tilde 块检出后底边框画不出（详见 3.3-1）。

#### 3.1.2 主循环重构（detect.go DetectSegments）

修改后的目标伪代码（保持 issue #6 修复兼容）：

```go
// 初始化：visibleStart 可能落在 codeblock 中间，
// 从 buffer 头扫到 visibleStart-1 统计 fence 行奇偶：
//   奇数 → 在块内，codeblockStart = 最后一个 fence 行
//   偶数 → 在块外，codeblockStart = -1
codeblockStart := findOpenFenceBefore(buf, visibleStart)

for y := visibleStart; y <= visibleEnd; y++ {
    line := buf.LineBytes(y)
    trimmed := TrimSpace(line)

    if isFenceLine(trimmed) {
        if codeblockStart == -1 {
            // 开 fence：先关闭未闭合的 list/blockquote/table（issue #6 保持）
            closeOpenStructures(&segments, &state, startLine, y)
            codeblockStart = y
        } else {
            // 闭 fence：emit [codeblockStart, y]（RenderCodeBlock）
            codeblockStart = -1
        }
        continue // fence 行已归入 codeblock，不做字符串匹配
    }
    if codeblockStart != -1 {
        continue // 块内行：跳过，不产生独立 segment
    }
    // ↓ 与现状相同的字符串匹配（heading/table/list/blockquote/HR/normal）
    ...
}

// 兜底：codeblockStart != -1 → emit [codeblockStart, visibleEnd]
```

开 fence 分支调用的 `closeOpenStructures` 是把现有 detect.go 进入 codeblock 分支里的
switch state 块原样抽成函数（issue #6 行为不变，主循环/兜底两处可复用）：

```go
// closeOpenStructures 在进入 codeblock 前关闭未闭合的多行结构（issue #6）。
// list/blockquote/table 各 emit [startLine, y-1] 段，state 归 normal。
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

#### 3.1.3 回溯初始化 `findOpenFenceBefore`

```go
// findOpenFenceBefore 判断 visibleStart 落在哪个 codeblock 内：
// 从第 0 行扫到 visibleStart-1，遇到 fence 行就翻转块内/块外状态。
// 返回未闭合块的开 fence 行号；块外返回 -1。
// 当前生产热路径恒 visibleStart==0（buffer.go 全量调用），本函数零开销；
// 若未来 detect 改增量、visibleStart>0 进入热路径，需把结果缓存到 SharedBuffer。
func findOpenFenceBefore(buf BufferReader, visibleStart int) int {
	open := -1
	for y := 0; y < visibleStart && y < buf.LinesNum(); y++ {
		trimmed := strings.TrimSpace(string(buf.LineBytes(y)))
		if isFenceLine(trimmed) {
			if open == -1 {
				open = y   // 开 fence：进入块内
			} else {
				open = -1  // 闭 fence：回到块外
			}
		}
	}
	return open
}
```

逻辑说明：`open` 是开关变量——fence 行奇数次出现后非 -1（在块内，记住开 fence 行号），
偶数次后归 -1（块外）。这和主循环的开/闭判定用同一套 fence 规则，
回溯结果与主循环向前扫描的结果天然一致；visibleStart == 0 时循环不执行，直接返回 -1。

成本 O(visibleStart)，每次 DetectSegments 调用一次。注意生产路径 buffer.go 恒以
(0, LinesNum-1) 全量调用 DetectSegments（buffer.go:218、1060），visibleStart 恒为 0，此函数在生产路径零开销；
只有测试（如 TestDetectCodeBlockMidStart）和防御性用途会传非零 visibleStart。
若未来改为按可见区增量 detect 且大文件滚动有感知，再缓存到 SharedBuffer（初版不做）。

#### 3.1.4 state 从 detect 退役

- `BufferReader` 接口直接删除 `State()` 方法：md 包内只有 detect.go 用它（两处），接口收窄不影响实现方（*buffer.Buffer 结构化满足接口）；detect.go 与 detect_test.go 的 `pkg/highlight` import 一并移除，mockBuffer 去掉 State 方法。
- `detectState`（blockquote/table/list 状态机）保留，与本次无关。

### P1：renderSegmentMD 非码块段污染检测 + fresh 重高亮（切断颜色污染）

P0 修好后结构恢复，但 state 仍粘滞：`expandLineStyles`（bufwindow_md.go:1092）读 `w.Buf.Match(bufLine)`，粘滞的 `constant.string` 会把污染区之后的正文/标题行整行染色。

**约束（首版 P1 的错误已修正）**：普通 MD 文本的全部颜色都来自 highlight overlay——renderInline 只设文本属性（bold/italic/underline），颜色全靠 `expandLineStyles` 叠加 `md-header`/`md-list`/`md-blockquote`/`md-link`/`md-inline-code`/`md-bold`/`md-hr` 等组。因此**不能整体去掉非 codeblock 段的 overlay**，否则 MD 配色全清空。

**正确思路（附录B实验已验证）：污染检测 + 按需 fresh 重高亮**。实验证明：污染行的 Match 里只有整行一个 `constant.string`，没有任何 md-* 组（state 卡在字符串 region 后永不回落顶层 md 规则）；而 fresh 单行 highlight 对同一行给出完全正确的 md-* 组（markdown.yaml 的全部 md-* 规则都是单行 regex，不依赖跨行 state）。因此：

#### 3.2.1 Segment 增加字段

`internal/md/md.go` 的 `Segment` 增加：

```go
IsCodeBlock bool // detect 层标记：该段是否 codeblock
```

detect.go 中两处 emit codeblock（闭 fence 处、兜底未闭合处）设 `IsCodeBlock: true`，其余默认 false。

#### 3.2.2 expandLineStyles 污染检测 + fresh 重高亮

`internal/display/bufwindow_md.go` 的 `expandLineStyles` 增加段类型参数，逻辑：

```go
// isMDGroup：合法的顶层 md 组。Group 名为空（默认组）规为中性，不算污染。
func isMDGroup(g highlight.Group) bool {
	name := g.String()
	return name == "" || strings.HasPrefix(name, "md-")
}

// expandLineStyles 内（非 codeblock 段）：
match := w.Buf.Match(bufLine)
if polluted(match) {   // 任一非空且非 md-* 前缀的组 ⇒ state 粘滞污染
	match = freshLineMatch(line)  // HighlightString 单行重高亮，取正确 md-* 组
}
// 之后的展开循环不变
```

签名变化与调用点（renderSegmentMD:124 透传 `seg.IsCodeBlock`）：

```go
func (w *BufWindow) expandLineStyles(bufLine int, runeCount int, baseStyle tcell.Style, isCodeBlock bool) []tcell.Style
// isCodeBlock=true：跳过污染检测，直接用 buffer Match（嵌入语言组正常工作）

// freshLineMatch 不挂 BufWindow，便于独立单测：
func freshLineMatch(def *highlight.Def, line string) highlight.LineMatch
```

实现要点：
- `freshLineMatch` 用 md Def 新建**一次性 Highlighter**：`highlight.NewHighlighter(w.Buf.SyntaxDef)` 再调用 `HighlightString`。
  不复用 buffer 共享的 `w.Buf.Highlighter`（buffer.go:1053）——那是可变对象，`HighlightString` 会读写 `h.lastRegion`，
  显示线程与 buffer 编辑线程共享会引入竞态，且结果依赖进入时的 lastRegion。一次性 Highlighter 结构很轻（Def 指针 + lastRegion），
  单行输入 i==0 强制走 highlightEmptyRegion、从 nil 态开始，结果确定且正确。
- codeblock 段不走此路径（保持 buffer Match，嵌入语言组正常工作；块内粘滞是已知限制 P2）。
- 干净行（Match 全是 md-*）零开销，与现状完全一致；污染文档才付每行单行 regex 的代价，且仅尾屏可见行。
- 量化：fresh 只在污染行触发，上限 = 可见行数（一屏 ~50 行）。markdown 顶层 ~15 个 pattern + ~30 个 fence region start regex ≈ 45 次短行 regex/行，污染满屏 ≈ 2000+ 次/帧，微秒~低毫秒级，且仅打开被污染文档时发生。不做「fresh 结果写回 b.SetMatch」缓存——会覆盖 state 派生的真实 Match、污染 buffer 全局状态，得不偿失。

效果：T1 文档污染尾部恢复完整 md 配色（标题/列表/行内代码颜色全田），干净文档零成本。

### 3.3 顺带修复：isClosingFence 补 ~~~ 与 isHR 补 `*`/`_`

1. `render_codeblock.go` 的 `isClosingFence` 只认 ``` 不认 `~~~`。P0 让 tilde 块正确检出后，若不补，tilde 块会「上边框有、下边框无」（`isClosed` 判不出末行闭合）。改为与 `isFenceLine` 同源判定：

```go
func isClosingFence(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}
```

2. `detect.go` 的 `isHR` 只支持 `-`/`=`，markdown.yaml（`^(---+|===+|___+|\*\*\*+)\s*$`）与 TestDetectHR 均要求 `*`/`_`。一行改动，顺带修复 TestDetectHR：

```go
// isHR 判断水平分割线：- = * _ 任意一种字符重复 ≥3 次
func isHR(s string) bool {
	if len(s) < 3 { return false }
	c := s[0]
	if c != '-' && c != '=' && c != '*' && c != '_' { return false }
	for i := 1; i < len(s); i++ { if s[i] != c { return false } }
	return true
}
```

注意：`___` 同时是 fence 之外合法的 HR 形态，且 isListItem 的 `* ` 开头判定优先级已在主循环字符串匹配里处理（`___` 不匹配列表前缀），无冲突。

## 4. 明确不做

- **不改 highlight 引擎**：多行字符串 region 是合法特性，纯 .ts 文件打开也依赖它。
- **不批量改 80 个 yaml**：无法穷举落单引号场景，且 markdown.yaml 的 fence end 规则在字符串 region 内本来就不会被检查，改 yaml 治不了本。
- **P2（可选，本次不做）：codeblock 内残留污染**：落单引号所在的那一个代码块内部仍会从该行起染字符串色到块尾。与 micro 原生打开 .ts 的行为一致，接受。彻底方案（每块独立子 highlighter）成本高，不做。
- **Group.String() O(N) 反向索引优化（N=全局 Groups 表大小）**：String() 线性扫全局 Groups 表，属普适性能改进，与本计划解耦、另行处理。污染检测每行仅调几次 String()，量级可忽略，不构成本计划阻塞。

## 5. 测试与验收

### 5.1 单元测试

1. 恢复：P0 后 5 个 codeblock 相关测试自动恢复（见 2.4 表）；3.3-2 修复 TestDetectHR。
2. 新增用例：
   - `TestDetectCodeBlockWithLoneQuote`：```ts fence 内放 `/["}'']/g`（落单双引号）行 + 闭 fence + 后续 `### heading`，断言 codeblock 正确闭合、heading 正常成段。
   - `TestDetectCodeBlockMidStart`：`DetectSegments(buf, 2, 3)`，visibleStart 落在块中间，断言回溯初始化后仍判 codeblock。
   - `TestDetectFenceRestoreAfterLoneQuote`（回归锁定 P0 核心收益，对应 fixed3 实验）：```ts fence 内含落单引号行 + 闭 fence + 后续 `### heading`，断言该块在下一条 fence 处正确闭合、后续段落恢复为独立 segment、不再吞到 EOF。
   - `TestDetectListBeforeFence`：未闭合 list 后紧跟 fence，断言 list 在 y-1 关闭、无 segment 乱序（issue #6 行为保持，目前无专门用例）。
   - 缩进 fence（` ```python`）判定。
3. mockBuffer 删除 `State()` 方法（接口已删，见 3.1.4），detect_test.go 移除 highlight import。
4. P1 回归测试（internal/display，锁污染检测 + fresh 重高亮；没有这条 P1 改完无法验证）：
   - `TestIsMDGroup`：空组名中性；`md-*` 合法；`constant.string` 非法。
   - `TestPolluted`：{0: md-header} 不污染；{0: constant.string} 污染。
   - `TestFreshLineMatch`：用真实 markdown.yaml 的 Def 对 `### heading`、`- item` 单行 fresh，断言命中 md-header / md-list 组（freshLineMatch 不挂 BufWindow，可直接单测）。

### 5.2 已知独立问题（不在本计划范围，验收不要求修复）

- TestDetectBlockquoteWithEmptyLines / TestDetectMixedContent：blockquote 遇空行即闭合的语义与测试期望不一致，需另行决策（改代码还是改测试期望）。
- TestRenderTable_Simple：渲染层 separator 行 BufLine 语义问题。
- 验收标准：本计划范围内 = 上述 5 个 codeblock 测试 + TestDetectHR 恢复 PASS，其余 3 个 FAIL 保持现状不新增恶化。

### 5.3 手工验收

- T1 文档（含两类污染：双引号 + 模板字符串反引号）渲染恢复：159 块正常闭合、后续 `### 4/5/6` 标题与 fence 正常。
- 正常 md（`docs/sample.md` 等）回归无变化。
- 编辑模式进出 codeblock 回退原生渲染不受影响。
- `make build` 通过；`go test ./internal/md/ ./internal/display/` 全绿。

## 6. 风险与边界

| 风险 | 评估 |
|---|---|
| fence 字符串判定与 yaml fence region 语义偏差 | yaml 缩进限 ≤3 空格；helper 从宽（TrimSpace）。差异仅影响 >3 缩进的 fence 行（极罕见），不产生错误高亮，仅 detect 边界略宽 |
| 代码行以 ``` 开头但非 fence 的误判 | markdown 语义内不存在；块内 ``` 就是闭 fence，块外就是开 fence |
| 闭 fence 长度 < 开 fence、嵌套 fence（块内出现 ``` 开头行）、混合字符闭合（``` 开 ~~~ 闭） | `isFenceLine` 只看前缀、toggle 语义，会提前闭合。markdown 语义要求同字符且闭 ≥ 开；但 markdown.yaml 的 fence end regex 同样是任一 fence 行终结 region、不校验长度/字符，detect 与高亮引擎行为一致，非新引入不一致。极罕见，接受为已知边界 |
| findOpenFenceBefore 每帧 O(visibleStart) | 常规文档可忽略；大文件实测有感知再缓存（初版不做） |
| renderSegmentMD 行为变化 | 仅"是否叠加 highlight 色"一点；非 codeblock 段 fallback 到 renderInline 样式，MD 格式（bold/inline-code/link）不丢 |
| editMode / renderSegmentNative 路径 | 不改。renderSegmentNative 按 segment 行范围原生渲染、不做 MD 分类，detect 只改变行的段归属（fence 内行跳过字符串匹配不影响其渲染），不受影响 |

## 7. 实施清单

1. `internal/md/detect.go`：新增 `isFenceLine`、`findOpenFenceBefore`、`closeOpenStructures`（issue #6 保持）；主循环改 fence 驱动；`BufferReader` 删 `State()`，移除 highlight import；`isHR` 补 `*`/`_`（3.3-2）。
2. `internal/md/md.go`：`Segment` 加 `IsCodeBlock bool`。
3. `internal/md/detect.go`：两处 codeblock emit 设 `IsCodeBlock: true`。
4. `internal/md/render_codeblock.go`：`isClosingFence` 补 `~~~`（3.3-1）。
5. `internal/md/detect_test.go`：跑恢复的 6 个测试；mockBuffer 删 State 方法；补 5.1 新用例。
6. `internal/display/bufwindow_md.go`：`expandLineStyles` 加 isCodeBlock 参数（调用点透传 seg.IsCodeBlock）+ 污染检测 + fresh 单行重高亮（3.2.2，非码块段专用）。
7. `internal/display` P1 单测：isMDGroup / polluted / freshLineMatch（真实 markdown.yaml，见 5.1-4）。
8. `make build` + 手工验收（T1 文档 + sample.md 回归 + md-* 配色不丢）。
9. 附录B实验已完成：污染行 Match 无 md-* 组实锤，P1 采用污染检测 + fresh 重高亮方案（详见附录B文档）。
