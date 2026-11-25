# 项目操作规则

> ⚠️ **AI 署名约定**（AI Signature Policy）：commit message 不带 AI 署名。
> 记录于 2026-11-24，触发场景：push origin main，检出 0 条署名。
> 约定：每次单独询问。
> 修改约定请直接编辑本行。

> ⚠️ **贡献者身份约定**（Author Identity Policy）：push 前统一作者身份。
> 记录于 2026-11-24，触发场景：push origin main，检出 1 个名单外作者，已统一改写。
> 修订于 2026-11-25（CR-004）：补上「一致」的判断逻辑——一致 = author **name 与 email 两者均相同**
> （只匹配一项不算）。检查目标分支历史 `git log --format='%an <%ae>' | sort -u`：
> 恰好一个身份 = 分支标准身份，新提交必须用之；多个身份或与分支标准身份不符 = 暂停问用户。
> 不写死任何具体身份。
> 约定：push 前必须统一作者。
> 修改约定请直接编辑本行。

> ⚠️ **CI 记录约定**（CI Recording Policy）：仓库内文档**永不记录 CI 成功**。
> 记录于 2026-11-25（CR-004），起因：「CI 对 commit X 绿」的提交自身是新 commit，其 CI
> 永远未被记录——自指循环逻辑上不可能完成（CR-003 曾因此连续两次追记文档）。
> 约定：push 后**等 CI 真正完成**（本项目 Go race 检测分钟级，进行中不下任何结论）；
> 绿 = 不存在当前方案可检测的错误，零动作零记录；红 = 当 bug 修，失败原因可作为缺陷
> 追踪记录（这不是验证声明，不产生自指），修复转绿即关闭、同样不记成功。
> CI 状态以 GitHub Actions 为唯一事实来源。docs/STATUS.md 验证表只记本地实测命令。
> 修改约定请直接编辑本行。

## 项目事实

- 目标：本地运行的 AI 中转站价格归一化计算器——把「官方价 × 折扣 × 上下文分档 × token 配比」归一化为可横向比较的每 1M tokens 单价，双币（不换汇/换汇）展示。用户：中转站（one-api/new-api 类）个人用户。非目标：抓取中转站实时价目、按次/多模态计费、多用户同步。
- 类型：桌面优先的本地 Web 工具。前端=纯 HTML+JS 单文件式（`public/index.html`）+ 独立纯计算模块（`public/calc.js`，供 node:test 复用）；后端=Python 标准库零依赖（`server/app.py` http.server）；抓取器=`server/fetcher.py`（官方 .md 适配器）+ `server/nodepool.py`（v2rayN 节点池，VLESS+Reality→HY2，独立 xray/sing-box 端口，不改 v2rayN 配置）；持久化=`data/config.json` JSON 文件；运行=`python -m server.app --port 8787`。
- 前端复杂度：低—中（数据表+表单工具，无实时/角色/动画要求）。
- 关键目录：`public/`（前端）、`server/`（Python 后端，参考实现）、`server-go/`（Go 版后端，单二进制部署目标）、`data/`（价目+运行时配置）、`tests/`、`design/wireframes/`、`docs/`。

## 文件地图与读写契约

AGENTS.md 只存框架性约定；**当前阶段/批准/验证状态/未决项等易变信息一律写 `docs/STATUS.md`，不写回本文件**。

| 文件 | 何时读 | 何时写 |
|---|---|---|
| `docs/STATUS.md` | **每次会话开始先读**（当前阶段/批准/验证/未决） | 批准后、命令实测后更新对应表 |
| `docs/PRD.md`、`docs/ARCHITECTURE.md` | 涉及需求/架构判断 | 对应实质变更随 CR 更新 |
| `docs/DUAL_IMPLEMENTATION.md` | **改 `server/` 或 `server-go/` 前必读**（等价性清单与已知坑） | 双端行为差异、等价条目、坑变化时更新 |
| `server-go/STATUS.md` | 改 `server-go/` 前 | server-go 会话状态与坑 |
| `docs/DEVELOPMENT.md` | 查命令/结构 | 命令、目录、开发流程变化 |
| `docs/DEPLOYMENT.md`、`server-go/DEPLOYMENT.md` | 部署与排障 | 部署方式、凭据、排障变化 |
| `docs/TEST_PLAN.md` | 验收/补测试 | 用例与冒烟清单变化 |
| `docs/PROJECT_DISCOVERY.md` | 查发现证据与冲突项（R2） | 新调研证据 |
| `docs/changes/CR-*.md` | 实质变更前（见变更控制） | 每实质变更一件；状态机 proposed/approved/rejected |

## 本项目阶段与交付

| 阶段 | 产物与验证 | 人工门 |
|---|---|---|
| 发现/需求/架构 | docs 五件套 + AGENTS + data/models.json | 确认范围、技术选择、阶段裁剪 |
| A 低保真 | `design/wireframes/` 可查看线框 + `docs/WIREFRAMES.md`（页面/入口/状态索引） | 功能与布局完整性，不定视觉 |
| B 高保真 | 建议跳过（工具型 UI，待批准） | — |
| C 示意 Demo | `public/calc.js`+`index.html` 可运行、localStorage 持久化、正常/空/加载/错误态可切换 + `docs/PRODUCT_BASELINE.md` | 确认产品形态 |
| 实现/联调 | `server/` API、汇率联网、导入导出；保持基线交互 | 影响基线走 CR |
| 验证 | `node --test tests/` + TEST_PLAN 冒烟记录 | 验收 |

产品基线批准前不实现 `server/`、`public/` 正式代码。映射表见 PRD §6。

## 角色与委派

默认角色来源约定为第三方 [msitarzewski/agency-agents](https://github.com/msitarzewski/agency-agents)；**本运行时（Devin）可调用子代理仅 `subagent_explore`（只读）与 `subagent_general`（读写）**，具名 agency 角色均不可调用（R6）。

| 阶段职责 | 请求角色完整名 | 来源与版本 | 运行时实际可用 | 请求/实际模型 | 缺失处理/授权依据 |
|---|---|---|---|---|---|
| 需求/文档 | Product Manager | agency-agents（未安装，版本 unknown） | 不可调用 | 未指定/— | 主线程执行（本阶段）；实现阶段是否降级 `subagent_general` 待用户确认 |
| 架构 | Software Architect | 同上 | 不可调用 | — | 同上 |
| 线框 | UX Architect | 同上 | 不可调用 | — | 同上 |
| 实现/测试 | 栈匹配实现角色 | 同上 | 不可调用 | — | 同上 |

恢复路径：用户在 Codex 侧安装角色后重载会话；或明确授权 `subagent_general` 降级（委派仍走子代理，不由主线程接管实现）。

## 编码与验证

- 格式化/lint：**不引入 Prettier/ESLint**（零依赖决策，待批准）；风格=2 空格、分号、camelCase、中文界面文案英文代码标识符（fallback，首文件确立后沿用）。
- 命名/目录/错误处理/日志/配置：见 ARCHITECTURE §3/§6、DEVELOPMENT §4。注释仅写非显然原因。
- 变更聚焦当前任务；不夹带无关重构/格式化；不提交运行产物（config.json、汇率缓存）与任何密钥。
- 关键命令（工作目录=仓库根）：

| 用途 | 命令 | 来源 |
|---|---|---|
| 启动 | `npm start`（= `python -m server.app --port 8787`；需先设 `AIPC_ADMIN_USERNAME`/`AIPC_ADMIN_PASSWORD`） | DEVELOPMENT §2 |
| 价目更新 | `npm run update-pricing`（= `python -m server.fetcher`） | DEVELOPMENT §2 |
| 节点池 | `python -m server.nodepool [N]` | DEVELOPMENT §2 |
| 测试 | `npm test`（= `node --test` + `python -B -m unittest discover -s server -t . -p "test_*.py"`） | package.json |
| Go 构建 | `cd server-go && go run ./tools/syncassets && go build -tags "with_utls with_quic" -o aipc-server.exe ./cmd/server`（tags 不可省：Reality/HY2 必需） | ARCHITECTURE §10 |
| Go 测试 | `cd server-go && go test ./internal/fetch` | ARCHITECTURE §10 |
| Go 交叉编译 | `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -tags "with_utls with_quic" -o aipc-server ./cmd/server`（tags 同上不可省） | ARCHITECTURE §10.6 |

未运行不宣称通过；报告区分本次引入/既存/环境限制。各命令最近一次实测结果记 `docs/STATUS.md`「命令验证状态」。

## Git 与交付约定

- 新仓库默认 `main`；短分支 `feat/<kebab>`/`fix/<kebab>`/`chore/<kebab>`，GitHub Flow + 独立 PR squash；Conventional Commits 1.0.0；无既有历史 → 提交信息用英文摘要（首条后保持一致）。（fallback，用户未指定）
- 先查工作区，保留他人改动；不擅自清理/强推/改写历史。
- 提交仅含本任务文件并看 staged diff；commit/push/merge/deploy 按当次授权执行。

## 变更控制

对已批准页面、流程、数据/权限/API、部署、产品基线的实质变更：先建 `docs/changes/CR-<id>.md`（原因、原/新行为、影响面、回滚、需重审门、proposed/approved/rejected + 批准证据）。用户明确授权的变更可作批准；仅创建 CR 不是批准。

## Definition of Done 与审核包

- 阶段产物与文档一致；可运行项附真实验证结果（未运行明确标注）；无未解释高优先级缺陷；变更范围可审阅。
- 每次完成输出一页内 Review Packet：完成与产物入口 / 验证与置信度 / ≤3 项待决定 / 风险 / 当前门与下一步。审核门处暂停，不自行进入下一阶段。
