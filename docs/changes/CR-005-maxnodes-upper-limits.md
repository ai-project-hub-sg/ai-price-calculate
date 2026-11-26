# CR-005 maxNodes 上限规则：超 999 提示输错、禁超 9999

- 状态：**approved for implementation**（用户 2026-11-26 明确指示：「maxNodes 不应该出现极限大值，如果 maxnodes 超过 999 需要对用户明确提示，是否输入出错。maxnodes 禁止超过 9999。」）
- 日期：2026-11-26
- 基线：`fix/go-port-review-fixes@d0e266f`（CR-003 + CR-004 完成态，未合并）
- 分支：继续 `fix/go-port-review-fixes`（合并时 squash 进 `feat/go-port`，作者 `notebook <ai-project-hub@outlook.com>` 已预确认）

## 背景

CR-004 把 maxNodes 语义统一为「纯上限无魔法值」后留下一个漏洞：上限本身无上限——`maxNodes=100000` 甚至 `1e20`（Python 侧任意大 int）都被静默接受。极限大值只可能是输入错误，且双端在极大值处行为分歧（Go ≥int64 报 400，Python 接受）。本 CR 增加两层上限规则。

## 原行为 / 新行为

| 输入区间 | 原行为 | 新行为 |
|---|---|---|
| 缺省 / 1–999 | 接受（缺省 999 等同全部） | 不变 |
| 1000–9999 | 静默接受 | 接受，但对用户**明确提示「是否输入出错？」**：POST 响应带 `warning` 字段 + job 日志首行警告（缓存命中路径同样提示）；fetcher/nodepool CLI 打印警告 |
| >9999 | 静默接受（Python）/ ≥int64 才 400（Go） | **禁止**：API 层 400 且报错含「是否输入出错？」；库层 `fetch.Update`/`fetcher._update` 报错兜底；CLI `ap.error`/`sys.exit` 拒绝 |

「整数值」规则不变：`9999.0` 接受（+警告），`9999.5`/`10000`/`1e20`/非数字 → 400。Go `parseMaxNodes` 中原 `v >= maxInt` 检查被 >9999 完全覆盖，删除。

## 影响面

- Go：`internal/httpsrv/server.go`（`parseMaxNodes`、新 `maxNodesWarn`、`hUpdate`/`hNodesTest` 响应与两个 runner 日志）；`internal/fetch/update.go`（库层硬上限 + `MaxNodesHardLimit` 常量 + Options 注释）。
- Python：`server/app.py`（`_max_nodes`、新 `_max_nodes_warning`、两个 POST 响应与 runner 日志）；`server/nodepool.py`（新增 `MAX_NODES_WARN`/`MAX_NODES_LIMIT` 常量——fetcher/app 同源引用，nodepool `__main__` 提示）；`server/fetcher.py`（`_update` 库层守卫 + CLI 提示）。
- 前端：不改动（前端不发 maxNodes；`warning` 为响应附加字段，旧前端忽略）。
- 文档：ARCHITECTURE §10.3/§10.5、DUAL_IMPLEMENTATION #10、TEST_PLAN 用例行、STATUS.md。

## 回滚

合并前 revert 本分支新增提交；合并后 revert squash 提交。

## 需重审门

提示文案改语义/阈值数值变化/新增输入面需重审；纯文案措辞调整不需要。

## 验收标准

- 双端矩阵一致：`999` 静默接受、`1000`/`9999` 接受并提示「是否输入出错？」、`10000`/`1e20` 400 且报错含提示、`9999.5` 400（非整数值）。
- `POST /api/pricing/update`、`/api/nodes/test` 响应在告警区间带 `warning` 字段；job 日志含同一警告（含 nodes/test 缓存命中路径）。
- `fetch.Update`/`fetcher._update` 对 >9999 报错（不经 API 也拦住）；`fetcher --max-nodes`、`nodepool N` CLI 同规则。
- `npm test`、Go 默认/tagged/vet 全绿。

## 验证记录

2026-11-26 在 `fix/go-port-review-fixes` 工作树完成（只记本地实测，CI 结果不入仓库）：

| 验证 | 结果 |
|---|---|
| `npm test` | 通过：Node 4 项、Python unittest 58 项 |
| `go test -count=1 ./...` / `-tags "with_utls with_quic"` / `go vet` | 全部通过 |
| maxNodes 矩阵双端一致（`TestParseMaxNodes` / `test_job_input_validation` + `test_max_nodes_*`） | 通过：`999` 静默、`1000`/`9999`/`9999.0` 接受、`10000`/`99999`/`1e20`/`9999.5` 400 且报错含「是否输入出错」与上限说明 |
| 告警到达面（`TestMaxNodesWarningSurfaces` / `test_max_nodes_warning_surfaces`） | 通过：响应 `warning` 字段 + job 日志首行（含 nodes/test 缓存命中路径）；`999` 不带 warning |
| 库层守卫（`TestUpdateRejectsOutOfRangeMaxNodes` / `test_update_rejects_over_limit`） | 通过：`fetch.Update`/`fetcher._update` 对 -1/10000/1e30 报错 |

限制：CLI 提示路径（`fetcher --max-nodes`、`nodepool N`）代码已加但本机未逐字跑过；前端不发 maxNodes，`warning` 字段为附加字段、旧前端忽略。
