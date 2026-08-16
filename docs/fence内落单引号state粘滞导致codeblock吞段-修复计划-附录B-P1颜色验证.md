# 附录B：P1 颜色来源验证 — 实验结果（work mode 已执行）

## 实验设置

/tmp/mdhl（独立 module，replace 指向 microNeo 源码）加载真实 markdown+全语言 yaml，
用真实 buffer 跑 `HighlightStates` + `HighlightMatches`，对比污染行与 fresh 单行 highlight 的 Match 组。

采样行（T1 文档，污染从 159 行 fence 内 169 行落单 `"` 开始）：
- 54：污染前的 `###` 标题（对照）
- 174：污染后的 `###` 标题
- 176：污染后的正文行（含 `` ` `` 行内代码）
- 238：污染后的 `##` 二级标题

## 结果

| 行 | 类型 | state 污染下的 Match | fresh 单行（对照） |
|---|---|---|---|
| 54 | 标题（污染前） | `{0:"", 3:"md-header"}` | 同左 ✓ |
| 174 | 标题（污染后） | `{0:"", 0:"constant.string"}` | `{0:"", 3:"md-header"}` |
| 176 | 正文（污染后） | `{0:"", 0:"constant.string"}` | `{0:"", x:"md-inline-code"×2}` |
| 238 | 标题（污染后） | `{0:"", 0:"constant.string"}` | `{0:"", 3:"md-header"}` |

## 结论：情形 B 实锤

1. **污染行的 Match 里没有任何 md-\* 组**，只有整行一个 `constant.string`。机制：state 卡在字符串 region 后，`highlightRegion` 只在 region 内找闭合引号，永不回落顶层的 md-\* 行级规则。
2. 因此审查者建议的"过滤污染组"若实现为"保留 md-\*、丢弃其它"：污染行过滤后无色可显 → 回落 DefStyle 白色。**正常行不受影响**（Match 本来全是 md-\*）。
3. **关键新发现**：fresh 单行 highlight 对同一行给出完全正确的 md-\* 组（markdown.yaml 的全部 md-\* 规则都是单行 regex，`^`/`$` 锚定，不依赖跨行 state；唯一的跨行构造是 fence region，而 fence 行属于 codeblock 段，不走这条路径）。

## 对 P1 设计的影响

纯过滤方案会让污染区之后的整个尾部（标题/列表/正文）失去 md 颜色、显示白色——结构对了但颜色仍是白的，正是用户原始抱怨的弱化版。结合发现 3，P1 升级为**污染检测 + 按需 fresh 重高亮**：

- 非 codeblock 段：检查该行 Match，若存在非 md-\* 组（Group 名非空且不带 `md-` 前缀）⇒ 判定污染 ⇒ 用 `HighlightString` 对该行单独重高亮，取其 md-\* 组
- 干净行零开销（直接用 buffer Match，与现状一致）
- codeblock 段：保持 buffer Match（嵌入语言组正常工作；块内粘滞为已知限制 P2）

效果：T1 文档污染尾部恢复完整 md 配色（标题青色、列表红色、行内代码红色），干净文档零成本。
