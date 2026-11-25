# CR-004 go-port 审查修复补完：胜出门槛、maxNodes 语义、数字写回与流程规则

- 状态：**approved for implementation**（用户于 2026-11-25 逐条确认修复方案 v3；**合并需二次授权**）
- 日期：2026-11-25
- 基线：`fix/go-port-review-fixes@5aa88d1`（= CR-003 修复分支顶端）
- 分支：`fix/go-port-review-fixes`（在 CR-003 基础上继续，合并时 squash 进 `feat/go-port`）

## 背景

对 CR-003 修复分支的复核发现：C2 只修了 Go 温缓存路径，冷探测路径与 Python 两端均未修；maxNodes 的「0 = 全部」文档表述在 Python 价目更新路径上实际为「0 = 一个都不探」；C9 数字修复把错误从 float 侧搬到了 int 侧（`64000` → `64000.0`）；验证流程上存在两个结构性错误决策。本 CR 补完这些修复并修正流程规则。

## 对 CR-003 错误决策的修正

1. **C9「禁止科学计数法」修正**：Python `json.dumps` 对 `0.0000001` 写 `1e-07`、对 `1e16` 写 `1e+16`——十进制-only 在极端值上恰恰偏离 Python。双端对拍的真正目标是**字节级一致**，正确做法是按 Python repr 规则写回，而非发明新格式。实际价目数据不会进入科学计数法量级，两种写法在现实数据上相同，但字节对拍测试要求严格一致。
2. **「仓库内记录 CI 结果」修正**：任何包含「CI 对 commit X 绿」的提交自身就是新 commit，其 CI 永远未被记录——自指循环在逻辑上不可能完成。CI 的正确用法是监控异常：绿 = 无需处理不记录；红 = 当 bug 修，失败原因作为缺陷追踪可以记录。CR-003 验证记录中的 CI 结果条目是最后一次此类记录。

## 修复需求

### E. CR-003 未完成的代码修复

| ID | 优先级 | 问题 | 期望行为 |
|---|---|---|---|
| E1 | 高 | 代理胜出校验不查字节数：Go 冷探测路径与 Python 两端均如此。无解析器源（moonshot）validate 直接 true，小 body（地区限制/反爬页）可胜出 → UI 显示已解决 → 最终 500B 检查丢弃 → 厂商既不合并也不在 unreachable，静默消失 | 字节门槛内嵌进胜出校验函数本身：Go `validate` 闭包（温/冷共用）与 Python `_validate_body` 检查 `len(body) > MIN_USEFUL_BODY`；小 body 不得胜出，目标留在 unreachable。双端补回归测试 |
| E2 | 中 | maxNodes 语义分裂：文档称「0 = 全部」，但 Python `find_working_proxy` 的 `nodes[:0]` = 空列表 = 一个都不探；且默认写死 25 与「需要测超过 25 个节点」的现实矛盾；Python 误杀整数值浮点 `25.0` | 语义唯一化：maxNodes = 最多探测节点数，纯上限无魔法值。缺省 999（实际等同全部）；正整数及整数值浮点（`25.0`→25）接受；`0`、负数、非整数值浮点（`25.1`——没有 25.1 个节点）、非数字 → 400。报错信息须说明什么是正确输入、用户输入为什么错。Python 库层默认值对齐，消除四条路径语义分裂 |
| E3 | 中 | canonical 写回对 integral float64 一律补 `.0`：int 数据（`contextWindow: 64000` 等）被写成 `64000.0`，与 Python 字节分歧。根因是 Go `encoding/json` 解码丢失 int/float 类型信息，任何只看值的格式化都数学上不可能同时写对两侧 | 类型保留解码：UseNumber + 按 Python 规则分流（整数词法 → int64，小数/指数词法 → float64）；写回按类型格式化，float64 按 Python repr 规则（含极端值科学计数法与两位指数补零）。`gen_fixtures.py` 增加 Python 写回字节基准 `merge_expected_file.json`，Go 测试升级为**逐字节对拍** |

### F. 流程规则（写入 AGENTS.md / DEVELOPMENT.md）

| ID | 问题 | 规则 |
|---|---|---|
| F1 | CI 结果入仓库导致自指循环 | CI 永不记录成功：push 后等 CI 完成（本项目 race 检测分钟级，进行中不下结论）；绿 = 不存在当前方案可检测的错误，零动作零记录；红 = 当 bug 修，失败原因可记录（缺陷追踪非验证声明），修复转绿即关闭。STATUS.md 验证表只记本地实测命令及对应代码状态 |
| F2 | 「作者一致」缺少判断逻辑 | 一致 = author **name 与 email 两者均相同**。检查目标分支历史 `git log --format='%an <%ae>'`：单一身份 = 分支标准身份；多个身份或不一致 = 暂停问用户。不写死任何具体身份 |

## 非目标

- 不改 CI workflow 文件（不引入 paths-ignore——文档变更未来可能需要触发文档校验脚本）。
- 不改写 `feat/go-port` 或修复分支历史；合并方式为正合 squash，不需要 force-push。
- 不处理 config.json 写回的数字格式（独立问题，影响面小，另行评估）。

## 验收标准

- mock 小 body 代理胜出场景：双端目标厂商留在 unreachable，不进 changed，UI 不误报已解决。
- 双端 `maxNodes` 测试矩阵一致：`25.0` 接受为 25、`25.1` 400、`0` 400、缺省 999；400 报错含正确输入说明。
- Go 写回字节 == Python 写回字节（merge fixture 逐字节对拍通过）；`64000` 保持 int、`1.0` 保持 float。
- `npm test`、Go 默认/tagged/race/vet、双平台构建全绿。
- AGENTS.md/DEVELOPMENT.md 规则更新；STATUS.md 无 CI 结果记录。

## 回滚

合并前放弃修复分支新增提交即可回到 `5aa88d1`；合并后 revert squash 提交。

## 验证记录

2026-11-25 在 `fix/go-port-review-fixes` 工作树完成（只记本地实测，CI 结果不入仓库）：

| 验证 | 结果 |
|---|---|
| `npm test` | 通过：Node 4 项、Python unittest 56 项 |
| `python -m compileall -q server`、前端 3 JS + inline script 语法 | 通过 |
| `go test -count=1 ./...` / `-tags "with_utls with_quic"` / `-race` / `go vet` | 全部通过 |
| `merge_test.go::TestMergeWritebackByteParityWithPython` 逐字节对拍 | 通过（Go 写回 == Python 写回字节） |
| `syncassets` + embed 与 `public/` diff | 零 diff |
| Linux/Windows amd64 tagged 构建 | 通过，56.2MB / 54.9MB，产物仅放临时目录 |
| mock 小 body 胜出回归（`TestValidWinnerBodySizeGate` / `TestValidateBodySizeGate`） | 通过：小 body 不得胜出，含无解析器 moonshot 场景 |
| maxNodes 双端矩阵（`TestParseMaxNodes` / `test_max_nodes_*`） | 通过：`25.0`→25、`25.1`/0/负数 400 且报错含正确输入说明、缺省 999 |
| `canonicalize` 正确性（`TestCanonicalize`） | 通过：内容零变化、幂等、tier 键序统一 |

限制：未访问真实订阅/凭据/运行价目更新；浏览器 UI 全流程、Windows 凭据管理器、Linux VPS 仍须人工验收。
