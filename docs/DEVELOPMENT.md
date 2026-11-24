# DEVELOPMENT — 环境、命令与约定

## 1. 环境

- Python ≥ 3.10（开发机 3.14.7）——后端/抓取器，零第三方依赖（`http.server`/`urllib`/`sqlite3`/`subprocess`）。
- Node.js ≥ 20（开发机 v24.21.0）——仅用于 `npm` 命令别名与 `tests/` 单元测试；无 npm 依赖、无构建步骤。
- 价目更新的节点池依赖本机 v2rayN（读 `%LOCALAPPDATA%/v2rayN` 的 SQLite 节点库与 `bin/` 下 xray、sing-box，全程只读不改配置）；无 v2rayN 时仍可直连抓取 + 人工核对。
- Windows 开发，路径与脚本以跨平台方式书写（不依赖 shell 特性）。

## 2. 命令

| 用途 | 命令（工作目录=仓库根） | 状态 |
|---|---|---|
| 启动后端+前端 | `npm start` 即 `python -m server.app --port 8787` | 已实现 |
| 官方价目更新 | `npm run update-pricing` 即 `python -m server.fetcher [--providers …] [--max-nodes N] [--dry-run]` | 已实现 |
| 节点池探测 | `python -m server.nodepool [节点数]` | 已实现 |
| 单元测试 | `npm test` 即 `node --test tests/` | 待实现 |
| 语法自检 | `python -m compileall server`、`node --check public/calc.js` | 部分可用 |
| 纯前端打开 | 直接双击 `public/index.html` | 待实现 |

当前阶段进度：`data/models.json`（145 模型、8 厂商口径、双时间戳）、`server/`（app/nodepool/fetcher）已实现并实测；`public/`、`tests/` 待建。

## 3. 开发顺序（获批后）

1. A 低保真：`design/wireframes/` 线框 + `docs/WIREFRAMES.md`（页面分区、交互、状态映射）→ **人工门**
2. C 示意 Demo：`public/calc.js` + `public/index.html` 可运行（含分档开关层级）→ **人工门（产品基线）**
3. 实现：前端接入后端 API、导入导出 → `docs/PRODUCT_BASELINE.md`
4. 验证：`tests/` + TEST_PLAN 全量执行记录

## 4. 代码约定

| 事项 | 约定 | 来源 |
|---|---|---|
| 语言/模块 | 浏览器 ESM 风格普通 `<script>`；`calc.js` 尾部队 `if (typeof module!=='undefined') module.exports=…` 以便 node:test 复用 | fallback：零依赖约束下的最小公共面 |
| 风格 | 2 空格缩进、单引号或双引号统一沿用首文件（建议双引号、分号、`camelCase`）；界面文案中文、代码标识符英文 | fallback（无既有代码） |
| 注释 | 仅解释非显然原因（公式口径、币种方向、档位语义） | 通用约定 |
| 格式化/lint | **不引入 Prettier/ESLint**（保持零依赖；若后期需要再经 CR 引入） | 决策（待批准） |
| 测试 | `node:test` + `assert/strict`；`tests/*.test.js` | fallback |
| 提交 | Conventional Commits（`feat:`/`fix:`/`chore:`/`docs:`），中文或英文摘要与首条历史保持一致；新仓库默认 `main` + 短分支 `feat/<kebab>` | fallback |
| 密钥 | 本项目不存任何 API Key；文档明确告知用户 | 安全约定 |

## 5. 数据维护约定

- `data/models.json` 由 `server/fetcher.py` 从官方 `.md` 页自动更新（手工微调亦可，需改 `meta.version` 并注明来源）。写回前自动备份 `models.json.bak`；`fetchedAt`=抓取日、`officialUpdatedAt`=官方生效日，两概念分开。
- 可自动抓取的官方源：OpenAI、Anthropic、Google、xAI、DeepSeek、Z.AI、bigmodel.cn（各家 `.md` 文档页）；Kimi 价目表 JS 渲染暂只能人工核对。直连失败走 v2rayN 节点池；不同厂商可能需不同出口节点。
- `data/config.json`、`data/fx-cache.json`、`data/models.json.bak`、`tmp_fetch/` 属运行时产物 → `.gitignore`，不提交。
- 价目结构变更（schema 增删字段）需同步 ARCHITECTURE.md 第 3 节与 calc.js 校验，向后兼容读取（缺省字段按约定回退）。

## 6. Git 约定

- 默认分支 `main`；短期 `feat/…`、`fix/…` 分支 + 独立 PR/squash（GitHub Flow）。
- 不提交运行产物与系统文件（见 .gitignore）；提交前看 staged diff。
- commit/push/merge 按当次用户授权执行，不默认获得权限。
