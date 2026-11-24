# PROJECT_DISCOVERY — AI 中转站价格归一化计算器

> 发现日期：2026-11-24。结论分「事实 / 推断 / 建议 / 待确认」，置信度标注于各项。

## 1. 当前事实

| 项 | 结论 | 证据 | 置信度 |
|---|---|---|---|
| 仓库状态 | 空仓库，仅有 `main` 分支、无任何 commit | `git status` / `ls` | 高 |
| 工作目录 | `E:\github\ai-price-calculate`，Windows 环境 | 系统信息 | 高 |
| 运行时 | Node v24.21.0 + npm 11.19.0；Python 3.14.7 亦可用 | `node -v` 等 | 高 |
| 目标用户 | 使用 AI API 中转站（one-api/new-api 类）的个人开发者/重度用户 | 用户需求描述 | 高 |
| 核心诉求 | 把「官方价 × 折扣 × 上下文分档 × token 配比」归一化为可横向比较的每 1M tokens 单价，并同时显示美元与人民币两个价格 | 用户需求描述 | 高 |

## 2. 需求梳理（来自用户输入 + 补充确认）

用户原始需求：

1. 收集 ChatGPT(OpenAI)、Claude(Anthropic)、Gemini(Google)、Grok(xAI)、DeepSeek、GLM(智谱)、Kimi(Moonshot) 常见模型的官方定价 —— **已完成调研并落为 `data/models.json`**。
2. 用户可配置：输入 / 输出 / 缓存读三类 token 的占比（token mix）。
3. 美元兑人民币汇率：可手动设置，也可联网自动获取。
4. 预设折扣比例；**不同中转站**可设不同默认折扣；**站内不同模型**可单独设折扣，甚至单独设置模型基础价格。
5. 归一化价格同时给出「不换汇（按计价币种原样）」与「换汇（折算为统一币种）」两个价格。
6. **补充（用户追加）**：中转站/官方均可能按上下文长度分档定价，需要可配置 —— 已采纳为数据模型的原生 `tiers` 结构 + 场景参数 `promptTokens`。

已确认的关键决策（2026-11-24，用户选择）：

| 决策项 | 选择 |
|---|---|
| 前端形态 | 纯 HTML + JS，无构建工具（单文件式） |
| 持久化 | 前端 + 轻量后端（后端持久化配置） |
| 中转站数据 | 用户自定义为主 + 内置示例预设 |

## 3. 技术判断

### 3.1 技术栈（建议 → 待用户批准）

- **后端：Node.js 零依赖 `node:http` 服务**（建议，置信度中）。
  - 理由：前端为纯 JS，同一语言生态；API 面仅 4~5 个端点（静态文件、配置读写、模型数据、汇率代理）；零第三方依赖 = 无 supply chain / 无锁文件维护 / `node server/index.js` 直接启动；Node 24 自带 `fetch`、`node:test`、`structuredClone`。
  - 代价：手写路由/静态服务约百行代码，无框架生态红利 —— 本项目体量可接受。
  - 备选：Express（若未来 API 变复杂）、Python FastAPI（用户无此倾向，且双语言增加心智负担）。
- **前端：`public/index.html`（单文件 SPA，内联样式与交互脚本）+ `public/calc.js`（纯计算模块）**。
  - `calc.js` 独立成文件的唯一理由：让归一化公式可被 `node:test` 单元测试而不依赖 DOM。无框架、无构建、无 npm 前端依赖。
  - 持久化策略（建议）：以 localStorage 为即时缓存，后端 `/api/config` 为权威存储；后端不可达时降级为纯 localStorage 并提示。多设备/多浏览器以服务端为准。
- **持久化：`data/config.json` JSON 文件**（原子写入：临时文件 + rename）。
  - 单用户本地工具，无并发、无关系查询需求 → 不需要数据库（最终结论）。SQLite 为过度设计。
  - `data/config.json` 不入库（gitignore）；`data/models.json`（官方价目）入库版本化。

### 3.2 前端复杂度：低—中

- 单页、无路由跳转需求（页内分区/页签）；无实时协作、无角色权限、无动画要求。
- 复杂度来源：结果表（厂商分组 + 每站双币列 + 档位命中提示）、中转站编辑表单（含按模型分档覆盖）。属「数据表 + 表单」型工具，非高交互应用。
- 含义：低保真（布局线框）→ Demo 两道人工门即可满足验证需求；高保真视觉稿默认建议**合并/跳过**（工具型 UI，无品牌要求），待用户确认。

### 3.3 关键设计事实（已核实，写入价目数据）

- 计价币种不一致：OpenAI/Anthropic/Google/xAI/DeepSeek/Z.AI 为 **USD**；Kimi、bigmodel.cn 国内站为 **CNY** → 价格模型必须携带币种，换汇双向折算（USD×rate→CNY；CNY÷rate→USD）。
- **上下文分档为普遍现象**（用户追加需求的官方侧证据）：
  - OpenAI 6.x：输入 >272K 时全请求升档（入/缓存翻倍、输出 ×1.5）；
  - Gemini 2.5 Pro / 3.1 Pro：prompt >200K 升档；
  - Grok 4.x：prompt ≥200K 升档；
  - Claude Haiku 5.5：prompt（含缓存）>100K 升档（$0.10/$0.50 → $0.50/$2.50）；
  - 智谱国内 GLM-4.7：按输入长度分档，且同输入档内还按输出长度再分价 —— 本工具以 promptTokens 档位近似，输出长度细分档在 v1 不展开（备注说明）。
- 缓存写价（5m/1h、30m/12h）在 Claude/Kimi/OpenAI 普遍存在 → 数据集保留 `cacheWrites` 字段（信息展示），但 v1 归一化配比只含 输入/缓存读/输出 三项（用户指定范围）。
- DeepSeek 有高峰/非高峰 5 折时段 → 模型级 `offPeakRatio` 字段 + 场景开关。
- Anthropic 新 tokenizer（Claude 4.7+）同文本多 ~30% tokens → 归一化比价时的重要口径差异，文档与 UI 需提示而非换算（token 数口径不同无法简单折算）。
- 汇率源：`open.er-api.com/v6/latest/USD`（免 key、每日更新、需署名链接），备选 `api.frankfurter.dev`（ECB 日度、免 key、CORS 开放）；后端代理 + 本地缓存避免 CORS/限流。

## 4. 风险与未决项

| # | 事项 | 类型 | 影响 |
|---|---|---|---|
| R1 | 官方价目会过期（促销截止、模型下线频繁） | 风险 | 数据文件标注 checkedAt；应用内允许编辑官方价作为兜底 |
| R2 | Gemini 3.1 Pro ≤200K 价格两来源冲突（$2/$12 vs 促销 $1.35/$6.75） | 待确认 | 已按 ai-magazine 填 $2/$12 并备注；实现期建议人工复核官方页 |
| R3 | OpenAI 5.x/6.x mini 类（如 gpt-6.x-mini、o4-mini）未取得官方价 | 风险 | 未收录；用户可自行补充 |
| R4 | 国内 GLM 输出长度子档（<0.2K）无法用 promptTokens 单参表达 | 决策 | v1 用 promptTokens 近似 + notes 说明；如需精确再在场景加 outputTokens 参与选档 |
| R5 | 跳过高保真视觉稿阶段 | 待确认 | 工具型 UI 建议直接 线框→Demo；等用户批准 |
| R6 | agency-agents 具名角色在本运行时不可调用 | 事实 | 可用子代理仅 `subagent_explore`/`subagent_general`；委派降级方式待用户确认 |

## 5. 阶段配置建议（待用户批准）

| 阶段 | 产物 | 人工门 |
|---|---|---|
| 发现/需求/架构（本阶段） | 本文档、PRD、ARCHITECTURE、DEVELOPMENT、TEST_PLAN、AGENTS、models.json | **当前停在此门**：确认范围、技术选择、阶段裁剪 |
| A 低保真 | `design/wireframes/`（ASCII/线框图）+ `docs/WIREFRAMES.md` | 布局/功能完整性 |
| B 高保真 | *建议跳过*（R5） | — |
| C 示意 Demo | `public/` + `server/` 可运行应用（模拟数据亦可），`docs/PRODUCT_BASELINE.md` | 确认最终形态 |
| 实现/联调 | 真实配置持久化、汇率联网、全部交互 | 按计划执行 |
| 验证 | 单元测试（calc）+ 冒烟 + TEST_PLAN 结果记录 | 验收 |
