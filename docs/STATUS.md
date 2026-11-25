# 项目状态（阶段 / 批准 / 验证 / 未决）

> 易变项目状态的**唯一**存放点。读写契约见 `AGENTS.md`「文件地图」：会话开始先读本文件；批准后、命令实测后更新对应表；**进度不写回 AGENTS.md**。

## 当前阶段与审核记录

| 字段 | 当前值 |
|---|---|
| 当前阶段/状态 | 实现/联调 —— 双后端 + 鉴权已合入 `feat/go-port`（CI 绿），**待本机 UI 全流程核验后 PR `main`** |
| 本阶段允许修改 | 全仓；对已批准行为/数据/API/部署的实质变更先建 `docs/changes/CR-*.md` |
| 审核产物与版本 | `docs/changes/CR-002-hardening-auth.md`（approved）；`feat/go-port` @ `720ed41` 三 run CI 全绿 |
| 最近明确批准 | 跳过高保真；五级层级与多档案汇总页等早期线框批准；CR-002 加固（登录/Bearer、URL 双重校验、原子写、更新串行化、停机竞态修复）squash 合并；等价性瑕疵修复（PBKDF2 对齐、URL 规范化、空 config 拒绝、`{"url":null}` 拒绝）FF 合并；登录限流共享桶保持现状并文档注明；pre-push 作者钩子已装 |
| 未决问题/风险 | R1 价目时效（F11 缓解）；Kimi 价目 JS 渲染只能人工核对；R6 角色降级方式待确认；Windows 凭据管理器真实读取与 Linux VPS/systemd/nginx 端到端未验证；`origin/fix/go-port-hardening` 远端分支处置待定 |
| 下一阶段及进入条件 | `feat/go-port` → `main` PR 验收：需本机登录/退出/非法订阅/配置保存/节点测试/价目更新/关机全流程核验通过 + push/PR 授权 |

不得将文件存在、自评通过或用户沉默当作批准。批准后更新本表；产物实质变更需确认原批准是否适用。

## 命令验证状态

只记最近一次实测结果（未运行不宣称通过；区分本次引入/既存/环境限制）。

| 命令 | 最近验证 |
|---|---|
| `npm start`（python -m server.app） | ✅ 本机实测：无凭据 fail-closed 拒起（exit 1）；env 凭据起服后 login→Bearer→logout 全链路通 |
| `npm run update-pricing` | ✅ 已实测：8 源全通、145 模型合并写回（加固前版本） |
| `python -m server.nodepool [N]` | ✅ 已实测：vless-reality/hy2 候选正确、xray 端口可达 |
| `npm test` | ✅ Node 3 项 + Python unittest 42 项全过 |
| Go 构建（syncassets + with_utls with_quic） | ✅ 已实测：54.7MB 二进制、节点探测 15/25 可用 |
| `go test` / `-race` / `-tags with_utls with_quic` | ✅ 全绿（e9a240c 后本机实测，含 PBKDF2 对拍向量） |
| Go 交叉编译 Linux amd64 | ✅ CI 实测构建通过；Linux 目标机实跑未验证 |

## 遗留人工核验清单

- [ ] 本机 UI 全流程：登录/退出、非法与空订阅、配置保存、节点测试、价目更新、关机（CR-002 重审门）
- [ ] Windows 凭据管理器 `aipc-server/admin` 真实读取
- [ ] Linux VPS systemd/nginx + 真实代理订阅端到端
