# 项目操作规则

## 项目事实与文档入口

- 目标：本地运行的 AI 中转站价格归一化计算器——把「官方价 × 折扣 × 上下文分档 × token 配比」归一化为可横向比较的每 1M tokens 单价，双币（不换汇/换汇）展示。用户：中转站（one-api/new-api 类）个人用户。非目标：抓取中转站实时价目、按次/多模态计费、多用户同步。
- 类型：桌面优先的本地 Web 工具。前端=纯 HTML+JS 单文件式（`public/index.html`）+ 独立纯计算模块（`public/calc.js`，供 node:test 复用）；后端=Node 零依赖 `node:http`（`server/index.js`）；持久化=`data/config.json` JSON 文件（无数据库，明确结论）；运行=`node server/index.js`。
- 前端复杂度：低—中（数据表+表单工具，无实时/角色/动画要求）。
- 关键目录：`public/`（前端）、`server/`（后端）、`data/`（价目+运行时配置）、`tests/`、`design/wireframes/`、`docs/`。
- 每次开始先读：本文件当前阶段表 → `docs/PRD.md`、`docs/ARCHITECTURE.md`。
- 详细发现证据与待确认项：`docs/PROJECT_DISCOVERY.md`（含价目调研来源与冲突项 R2）。

## 当前阶段与审核记录

| 字段 | 当前值 |
|---|---|
| 当前阶段/状态 | 发现/需求/架构 —— **进行中，待审核** |
| 本阶段允许修改 | `docs/`、`data/models.json`、`design/`、根 AGENTS/README/package.json/.gitignore；`public/`、`server/`、`tests/` 暂不创建实现 |
| 审核产物与版本 | 首批文档+价目数据（工作区快照，未提交则以此文件集为准） |
| 最近明确批准 | 未批准（技术栈三问已答：单文件前端 / 前端+轻量后端 / 自定义+示例站） |
| 未决问题/风险 | R1 价目时效；R2 Gemini 3.1 Pro 价格冲突；R5 建议跳过高保真待确认；R6 角色降级方式待确认 |
| 下一阶段及进入条件 | A 低保真（`design/wireframes/` + `docs/WIREFRAMES.md`），需本阶段获批准 |

不得将文件存在、自评通过或用户沉默当作批准。批准后更新本表；产物实质变更需确认原批准是否适用。

## 本项目阶段与交付

| 阶段 | 产物与验证 | 人工门 |
|---|---|---|
| 发现/需求/架构 | docs 五件套 + AGENTS + data/models.json | 确认范围、技术选择、阶段裁剪（**当前**） |
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

| 用途 | 命令 | 来源 | 本轮验证状态 |
|---|---|---|---|
| 启动 | `npm start`（= `node server/index.js`） | DEVELOPMENT §2 | 未实现 |
| 测试 | `npm test`（= `node --test tests/`） | DEVELOPMENT §2 | 未实现 |
| 语法自检 | `node --check <file>` | DEVELOPMENT §2 | 未实现 |

未运行不宣称通过；报告区分本次引入/既存/环境限制。

## Git 与交付约定

- 新仓库默认 `main`；短分支 `feat/<kebab>`/`fix/<kebab>`/`chore/<kebab>`，GitHub Flow + 独立 PR squash；Conventional Commits 1.0.0；无既有历史 → 提交信息用英文摘要（首条后保持一致）。（fallback，用户未指定）
- 先查工作区，保留他人改动；不擅自清理/强推/改写历史。
- 提交仅含本任务文件并看 staged diff；commit/push/merge/deploy 按当次授权执行。

## 变更控制

对已批准页面、流程、数据/权限/API、部署、产品基线的实质变更：先建 `docs/changes/CR-<id>.md`（原因、原/新行为、影响面、回滚、需重审门、proposed/approved/rejected + 批准证据）。用户明确授权的变更可作批准；仅创建 CR 不是批准。

## Definition of Done 与审核包

- 阶段产物与文档一致；可运行项附真实验证结果（未运行明确标注）；无未解释高优先级缺陷；变更范围可审阅。
- 每次完成输出一页内 Review Packet：完成与产物入口 / 验证与置信度 / ≤3 项待决定 / 风险 / 当前门与下一步。审核门处暂停，不自行进入下一阶段。
