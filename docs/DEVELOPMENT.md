# DEVELOPMENT — 环境、命令与约定

## 1. 环境

- Node.js ≥ 20（开发机 v24.21.0）；无 npm 依赖、无构建步骤。
- Windows 开发，路径与脚本以跨平台方式书写（不依赖 shell 特性）。

## 2. 命令

| 用途 | 命令（工作目录=仓库根） | 状态 |
|---|---|---|
| 启动 | `npm start` 即 `node server/index.js` | 待实现（Demo 阶段） |
| 单元测试 | `npm test` 即 `node --test tests/` | 待实现 |
| 语法自检 | `node --check server/index.js`、`node --check public/calc.js` | 待实现 |
| 纯前端打开 | 直接双击 `public/index.html` | 待实现 |

当前阶段（发现）已完成：`data/models.json`、docs、AGENTS；`server/`、`public/`、`tests/` 在获批后的 Demo/实现阶段创建。

## 3. 开发顺序（获批后）

1. A 低保真：`design/wireframes/` 线框 + `docs/WIREFRAMES.md`（页面分区、交互、状态映射）→ **人工门**
2. C 示意 Demo：`public/calc.js` + `public/index.html` 可运行（内置 models.json 读取、localStorage 持久化）→ **人工门（产品基线）**
3. 实现：`server/index.js`（静态+API+汇率代理）、前端接入后端、导入导出 → `docs/PRODUCT_BASELINE.md`
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

- `data/models.json` 手工维护：更新时改 `meta.version` 与各 provider `checkedAt`，在 notes 写明来源；PR 附官方页链接。
- `data/config.json`、`data/exchange-rate-cache.json` 属运行时产物 → `.gitignore`，不提交。
- 价目结构变更（schema 增删字段）需同步 ARCHITECTURE.md 第 3 节与 calc.js 校验，向后兼容读取（缺省字段按约定回退）。

## 6. Git 约定

- 默认分支 `main`；短期 `feat/…`、`fix/…` 分支 + 独立 PR/squash（GitHub Flow）。
- 不提交运行产物与系统文件（见 .gitignore）；提交前看 staged diff。
- commit/push/merge 按当次用户授权执行，不默认获得权限。
