# ai-price-calculate

AI 中转站价格归一化计算器：把「官方价目 × 自定义基价 × 折扣链 × 上下文分档 × token 配比（输入/缓存读/缓存写/输出）× 充值比例」归一化为可横向比较的**每 1M tokens 人民币成本**。

中转站常见玩法——把官方美元数字按人民币收（无视汇率）、1 元充 15~20 代币、基价注水——本工具全部建模，方便对比"走中转 vs 官方原价"的真实价差。

## 快速启动

所有后端 API 均需登录。先设置 `AIPC_ADMIN_USERNAME` 和 `AIPC_ADMIN_PASSWORD`（Windows 也可使用凭据管理器目标 `aipc-server/admin`）；缺少任一项时服务拒绝启动，不提供默认密码。详见 [部署指南](docs/DEPLOYMENT.md)。

```bash
# 方式 A：Python ≥ 3.10，零第三方依赖
export AIPC_ADMIN_USERNAME=admin
export AIPC_ADMIN_PASSWORD='请替换为足够长的随机密码'
python -m server.app          # → http://127.0.0.1:8787
npm start                     # 同上（需 Node，仅为别名）

# 方式 B：Go 单二进制（适合部署，内嵌前端与节点池）
cd server-go && go run ./tools/syncassets && go build -tags "with_utls with_quic" -o aipc-server ./cmd/server
./aipc-server                 # → http://127.0.0.1:8787
# -tags 不可省：Reality 要 with_utls、Hysteria2 要 with_quic，缺了全部节点出站初始化失败
```

打开 `http://127.0.0.1:8787/`，用配置的账号密码登录。令牌仅保存在当前标签页的 sessionStorage，有效期 12 小时。直接双击 `public/index.html` 仍可离线使用演示数据与 localStorage，但 API 功能不可用。

## 功能一览

- **结果明细表**：配置×站×提供商×模型跨档案铺行；中转站价格(¥) / 官价(原币) / 官价换汇(¥) 三价并列；配比列显示每行命中的实际配比；点列头排序、分面多选联动筛选
- **多档案**：新建/复制/重命名/删除/导入导出，互不覆盖；演示数据一键载入/清空
- **中转站树**：站点 → 提供商 → 模型覆盖三级；站点有充值比例（¥1→N代币）、提供范围上界；提供商有范围/默认折扣/厂商级折扣/厂商级分档开关/配比
- **范围级联选择器**：厂商→系列→模型三列向右展开，三态勾选、分级全选/反选；提供商可选项自动被站点范围裁剪
- **折扣链**：模型例外 > 厂商级 > 提供商默认 > 站默认 > 1，支持 >1 溢价（如 luna ×10）
- **配比分层**：提供商 > 站 > 模型覆盖 > 模型级预设 > 厂商级预设 > 档案场景，默认 4/95/0/1；内置社区实测预设（编码智能体 ~95% 缓存命中等 5 组场景 + 8 厂商）
- **计价要素**：上下文分档（操作符编辑+重叠/缺漏校验）、缓存写双 TTL（5m/1h）、闲忙时窗口（DeepSeek UTC 01-04/06-10 工作日）、非高峰折扣、CNY 原生币种
- **账单核对**：贴入 CSV（时间/站/商/模型/用量/实际扣费/备注），按备注语义判 1h 缓存写与忙闲时，比对官方窗口标黄，理论 vs 实际差值+可调误差判据
- **官方价目**：145 模型 8 厂商全表 + 一键更新（后台 job 轮询 + 实时进度卡；节点池：Python 用 v2rayN 库、Go 用代理订阅，1h 可用性缓存）+ UI 一键关闭后端

## 文档

| 文档 | 内容 |
|---|---|
| `docs/USER_MANUAL.md` | 使用手册（功能操作详解） |
| `docs/DEPLOYMENT.md` | 部署指南（运行方式/依赖/数据与配置/更新） |
| `docs/PRD.md` | 产品需求 |
| `docs/ARCHITECTURE.md` | 架构与数据模型 |
| `docs/DEVELOPMENT.md` | 开发环境与约定 |
| `docs/TEST_PLAN.md` | 测试计划 |
| `docs/DUAL_IMPLEMENTATION.md` | **双实现维护规则（改 server/ 或 server-go/ 前必读）** |
| `docs/EXPLICIT_HARDENING.md` | 隐式行为显式化登记与一致性测试矩阵 |
| `server-go/README.md` | Go 版快速开始/构建/测试/目录 |
| `server-go/DEPLOYMENT.md` | Go 版 Linux VPS + systemd + nginx 部署 |
| `AGENTS.md` | 项目工作规则 |

## 技术

纯 HTML+JS 前端（`public/`，无构建）+ Python 零依赖后端（`server/`，`http.server`/`urllib`）。配置存 `data/config.json`（后端模式）或 localStorage（file:// 模式）。
