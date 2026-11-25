# 项目状态（阶段 / 批准 / 验证 / 未决）

> 易变项目状态的**唯一**存放点。读写契约见 `AGENTS.md`「文件地图」：会话开始先读本文件；批准后、命令实测后更新对应表；**进度不写回 AGENTS.md**。

## 当前阶段与审核记录

| 字段 | 当前值 |
|---|---|
| 当前阶段/状态 | 实现/联调 —— 独立审查修复位于 `fix/go-port-review-fixes`（基线 `feat/go-port@83b6c06`），本地验证通过，**待 CI 与用户审核；未合并、未申请 PR** |
| 本阶段允许修改 | 全仓；对已批准行为/数据/API/部署的实质变更先建 `docs/changes/CR-*.md` |
| 审核产物与版本 | `docs/changes/CR-003-go-port-review-fixes.md`（approved for implementation）；待填写修复提交 SHA 与对应 CI run |
| 最近明确批准 | 用户于 2026-11-25 授权从最新 `go-port` 开独立分支、撰写需求与方案、修复并推送触发 CI；明确要求不得直接合并 `go-port`，由用户审核合并。早期线框与 CR-002 批准继续有效。 |
| 未决问题/风险 | R1 价目时效（F11 缓解）；Kimi 价目 JS 渲染只能人工核对；R6 角色降级方式待确认；登录后完整 UI、Windows 凭据管理器真实读取、Linux VPS/systemd/nginx 与真实订阅端到端未验证；`origin/fix/go-port-hardening` 远端分支处置待定 |
| 下一阶段及进入条件 | 用户审核 `fix/go-port-review-fixes` 的 diff、CI 与人工验收结果后自行合并 `feat/go-port`；后续 PR `main` 与部署需另行授权 |

不得将文件存在、自评通过或用户沉默当作批准。批准后更新本表；产物实质变更需确认原批准是否适用。

## 命令验证状态

只记最近一次实测结果（未运行不宣称通过；区分本次引入/既存/环境限制）。

| 命令 | 最近验证 |
|---|---|
| 隔离 Python/Go 鉴权 API 冒烟 | ✅ 2026-11-25 本机实测：静态页、未登录 401、登录、models、config 写入/回读、非法 JSON 400、退出后 401 全通过；使用测试凭据与临时数据目录 |
| `npm test` | ✅ 2026-11-25：Node 4 项 + Python unittest 45 项全过 |
| Go 构建（syncassets + with_utls with_quic） | ✅ 2026-11-25：Windows 约 53MB、Linux amd64 约 54MB；embed 与 `public/` 零 diff |
| `go test` / `-race` / `-tags with_utls with_quic` / `go vet` | ✅ 2026-11-25 在 `fix/go-port-review-fixes` 全部通过，含 PBKDF2 对拍向量与新增回归 |
| Linux amd64 交叉编译 | ✅ 2026-11-25 本机构建通过；Linux 目标机实跑未验证 |
| `npm run update-pricing` / `python -m server.nodepool [N]` | 历史版本已实测；本轮未访问真实订阅或运行价目更新，不能代替目标环境验收 |

## 遗留人工核验清单

- [ ] 本机 UI 全流程：登录/退出、非法与空订阅、配置保存失败可见、节点测试、价目更新、关机（CR-003 重审门；本轮仅完成隔离 API 冒烟）
- [ ] Windows 凭据管理器 `aipc-server/admin` 真实读取
- [ ] Linux VPS systemd/nginx + 真实代理订阅端到端
