# 部署指南

零依赖、无构建：Python 后端托管静态页 + API + 官方价目更新；也可完全离线双击 html 使用。

## 1. 运行要求

| 依赖 | 版本 | 用途 |
|---|---|---|
| Python | ≥ 3.10 | 后端服务与价目抓取（零第三方包） |
| Node.js | ≥ 20（可选） | `npm` 命令别名、`node --check` / 单测 |
| v2rayN | （可选） | 官方价目更新的节点池；无则直连 + 人工核对 |

## 2. 启动方式

### 方式 A：后端模式（推荐）

```bash
python -m server.app              # 默认端口 8787
python -m server.app --port 9000  # 指定端口
npm start                         # 等价（需 Node）
```

浏览器打开 `http://127.0.0.1:8787/`。

- 静态文件服务根 = `public/`；API = `/api/*`（见 ARCHITECTURE.md）
- 所有响应带 `Cache-Control: no-store`；页面左上角有版本戳 `Demo vX.Y · <commit>`，对不上说明浏览器拿了旧缓存（Ctrl+F5）
- Windows 下同端口可双绑：若行为异常先 `netstat -ano | findstr :8787` 确认没有残留旧进程

### 方式 B：纯前端（file://）

直接双击 `public/index.html`。无后端时自动降级：数据集用内嵌小样本、配置存 localStorage、汇率手动、「一键更新」提示后端不可达。

## 3. 数据与配置

| 文件 | 内容 | 说明 |
|---|---|---|
| `data/models.json` | 官方价目（145 模型 / 8 厂商） | 含分档 tiers、cacheWrites 双 TTL、peakWindows（DeepSeek 闲忙窗口） |
| `data/config.json` | 用户配置（档案树） | 后端模式下由 PUT /api/config 写入；删除即回演示数据 |
| `public/demo-config.js` | 演示档案 | 仅在无用户配置时载入；「载入演示」按 id 判重不覆盖 |
| localStorage `aiPriceCfg` | 用户配置 | file:// 模式的存储位置 |

- **备份**：直接复制 `data/config.json`（或用页面「导出」生成 JSON）
- **迁移**：file:// 与后端配置互不相通；换模式请「导出→导入」

## 4. 官方价目更新

```bash
# 页面内：官方价目页签 → 一键更新（后台 job，日志实时可见）
# 命令行：
python -m server.fetcher                          # 全部厂商
python -m server.fetcher --providers openai       # 指定
python -m server.fetcher --max-nodes 8 --dry-run  # 限节点/演练
python -m server.nodepool 8                       # 探测节点池
```

更新走 v2rayN 节点池（只读其 SQLite 节点库与 bin/，不改配置）；无节点池时直连。官方页返回 403/连接重置的厂商标记 `autoFetchReason`，需人工核对。

## 5. 验证

```bash
node --check public/calc.js        # 前端逻辑语法
python -m compileall server        # 后端语法
node -e "const C=require('./public/calc.js');const ds=require('./data/models.json');const r=C.summaryRows(require('./public/demo-config.js')||global.window&&0,ds,7.1)"  # 冒烟
npm test                           # node --test tests/（待补）
```

## 6. 目录结构

```
public/index.html   单页应用（含内联 JS/CSS）
public/calc.js      纯函数计算模块（可无 DOM 单测）
public/demo-config.js 演示档案
server/app.py       HTTP 服务 + /api/*
server/fetcher.py   官方价目抓取/合并
server/nodepool.py  v2rayN 节点池
data/models.json    官方价目数据集
data/config.json    用户配置（运行时生成）
docs/               PRD/架构/开发/测试/手册/部署
```
