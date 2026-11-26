# 项目状态（阶段 / 批准 / 验证 / 未决）

> 易变项目状态的**唯一**存放点。读写契约见 `AGENTS.md`「文件地图」：会话开始先读本文件；批准后、命令实测后更新对应表；**进度不写回 AGENTS.md**。

## 当前阶段与审核记录

| 字段 | 当前值 |
|---|---|
| 当前阶段/状态 | 实现/联调 —— `fix/go-port-review-fixes`（基线 `feat/go-port@83b6c06`）：CR-003 审查修复 + CR-004 补完 + CR-005（maxNodes 上限：>999 提示输错、>9999 拒绝，双端+CLI）。2026-11-26 用户已初评 d0e266f 并要求暂不合并、自审 diff；**未合并、未申请 PR** |
| 本阶段允许修改 | 全仓；对已批准行为/数据/API/部署的实质变更先建 `docs/changes/CR-*.md` |
| 审核产物与版本 | `docs/changes/CR-003` + `docs/changes/CR-004` + `docs/changes/CR-005-maxnodes-upper-limits.md`（均 approved for implementation） |
| 最近明确批准 | 用户于 2026-11-25 逐条确认 CR-004 修复方案 v3（含 maxNodes 999 缺省、双端 canonical 键序、CI 记录规则、作者一致性规则）；合并需二次授权。2026-11-26 用户确认将来 squash 作者 = 分支标准身份（`notebook <ai-project-hub@outlook.com>`），main 合并门未定（先只到 feat/go-port）。早期线框与 CR-002/CR-003 批准继续有效。 |
| 未决问题/风险 | R1 价目时效（F11 缓解）；Kimi 价目 JS 渲染只能人工核对；R6 角色降级方式待确认；登录后完整 UI、Windows 凭据管理器真实读取、Linux VPS/systemd/nginx 与真实订阅端到端未验证；`origin/fix/go-port-hardening` 远端分支处置待定；本地 `fix/go-port-followups` 为已并入 feat/go-port 的悬旧指针 |
| 下一阶段及进入条件 | 用户审核后授权 squash 合并 `feat/go-port`（作者身份按 AGENTS.md 一致性规则）；后续 PR `main` 与部署需另行授权 |

不得将文件存在、自评通过或用户沉默当作批准。批准后更新本表；产物实质变更需确认原批准是否适用。

## 命令验证状态

只记最近一次实测结果（未运行不宣称通过；区分本次引入/既存/环境限制）。

| 命令 | 最近验证 |
|---|---|
| 隔离 Python/Go 鉴权 API 冒烟 | ✅ 2026-11-25 本机实测（CR-003 阶段）：静态页、未登录 401、登录、models、config 写入/回读、非法 JSON 400、退出后 401 全通过；使用测试凭据与临时数据目录 |
| `npm test` | ✅ 2026-11-26（CR-005 代码状态）：Node 4 项 + Python unittest 58 项全过，无告警 |
| Go 构建（syncassets + with_utls with_quic） | ✅ 2026-11-25（CR-004）：Windows 54.9MB、Linux amd64 56.2MB；embed 与 `public/` 零 diff |
| `go test` / `-tags with_utls with_quic` / `go vet` | ✅ 2026-11-26（CR-005）6 包默认+tagged 全过、vet 干净。`-race` 最近一次 2026-11-25 通过，含 PBKDF2 对拍向量与 merge 写回逐字节对拍（`merge_expected_file.json`） |
| 前端语法检查 + `python -m compileall` | ✅ 2026-11-25（CR-004）：3 个 JS 文件 + 1 个 inline script、server/ 编译全通过 |
| Linux amd64 交叉编译 | ✅ 2026-11-25 本机构建通过；Linux 目标机实跑未验证 |
| `npm run update-pricing` / `python -m server.nodepool [N]` | 历史版本已实测；本轮未访问真实订阅或运行价目更新，不能代替目标环境验收 |

## 遗留人工核验清单

- [ ] 本机 UI 全流程：登录/退出、非法与空订阅、配置保存失败可见、节点测试、价目更新、关机（CR-003 重审门；本轮仅完成隔离 API 冒烟）
- [ ] Windows 凭据管理器 `aipc-server/admin` 真实读取
- [ ] Linux VPS systemd/nginx + 真实代理订阅端到端
