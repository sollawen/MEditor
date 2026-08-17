# R1a-性能优化lever-备忘

## 定位

本文是 R1（语法引擎 pattern 遮蔽修复）的后续分析备忘，记录 review 阶段提出、但首批不做的两个性能优化 lever：跨 pass 缓存、窗口化扫描。

**两个 lever 均不属于 R1 原计划范围**：R1 原计划只有 `findAllIndex` 版 shield 一种实现（见 R1 §3.1）。R1 已按该计划版落地，接受有界成本。

**等 R1 执行完之后，我们再来分析讨论 R1a 是否有必要来做。**

## 背景：实测数据摘要（详见 R1 §6.1）

| 场景 | 原版 | 计划版(FindAllIndex) | 窗口化版 |
|---|---|---|---|
| 561 行 TS 全量 | 7.66 ms | 14.20 ms（+85%） | 13.40 ms（+75%） |
| 1796 行 TS 合并 | 14.03 ms | 50.30 ms（+258%） | 46.96 ms（+235%） |
| 252 行 MD 全量 | 1.54 ms | 5.66 ms（+266%） | 4.99 ms（+223%） |
| 打字单行 | 12.8 µs | 26.9 µs（+111%，+14 µs/键） | — |

shield 成本集中在文件打开 / 大 undo / colorscheme reload 等低频一次性路径，导航类高频操作零影响。R1 决策：按计划版（FindAllIndex）落地，不做额外优化。

两个 lever 都不动遮蔽语义，仅改性能实现方式。均已分析到可执行程度。

## Lever 1 — 跨 pass 缓存 shield 决策（收益最大，有风险）

- **原理**：全量高亮时 `HighlightStates` 与 `HighlightMatches` 对同一行各跑一次 shield（每行 2× 开销）。若把 shield 决策按行号缓存（states pass 写入、matches pass 读取），可砍约一半成本。
- **收益估算**：全量场景从 +85%~+266% 降到约 +40%~+130%；打字路径本来只有一次，无收益。
- **实现要点**：给 `Highlighter` 加一个按 lineNum 的布尔缓存字段；states pass 填入、matches pass 读取；`ReHighlightLine`/`ReHighlightStates` 编辑增量时需失效被编辑行及其后行。
- **风险**：① 缓存生命周期与失效时机一旦错一次，state 与 matches 决策漂移——直接踩中 R1 §3.1 要点 3 的铁律（statesOnly 与非 statesOnly 必须一致），且比现在的无状态实现更难审计；② 增加引擎改动面（R1 当前 +27 行 → 更多），违背 R1「最小改动」优先级；③ 编辑/滚动并发下缓存与 buffer 状态的同步成本。
- **决策**：暂不做。若日后实测打开大文件有感，优先做此 lever，并新增类似 R1 §5.1 TestShieldStatesMatchesConsistent 的测试锁失效逻辑。

## Lever 2 — 窗口化扫描（低风险，收益小）

- **原理**：把 helper 内 `findAllIndex` 全量扫描换成「按序取 match、一旦 match 起点 ≥ pos 即停」的窗口化扫描（RE2 leftmost 语义下后续 match 起点只增不减，正确性等价），避免把每行扫到行尾。
- **已验证**：输出与计划版逐行完全一致（R1 三个用例全过），收益约 8~10 个百分点，helper 多约 +12 行。
- **决策**：非必须。若 Lever 1 落地后仍不足，再叠加切换。

## 明确不做

把 region 内 pattern 预编译成单个合并 regex 做一次性遮蔽检查——合并 `^` 锚点/捕获组会引入语义风险，且需改 parser 结构，违背最小化。
