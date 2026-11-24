# CR-001 新增 Go 版后端（server-go/）与节点获取方式变更

- 状态：**approved**（用户在会话中明确指示"开个分支做吧，直接把代理打包进 Go，允许 Web 界面填代理订阅地址；先直连，直连失败用代理，都失败报错"）
- 日期：2026-11-24 · 分支：`feat/go-port`

## 原因

需要把项目编译成单二进制部署到 Linux VPS + nginx 反代，免 Python 运行时；且节点获取不能依赖 Windows v2rayN。

## 原行为 → 新行为

| 项 | 原 | 新 |
|---|---|---|
| 后端 | 仅 Python `server/` | 双实现：Python（参考）+ Go `server-go/`（部署） |
| 节点来源 | nodepool.py 读 v2rayN sqlite + 拉起 xray/sing-box 子进程（仅 Windows） | 用户填订阅地址（`config.proxySubscription`）→ 进程内嵌 sing-box，零外部进程，跨平台 |
| 前端 | 无代理配置项 | 「官方价目」页新增订阅地址输入；更新结果逐源展示、失败红色提示 |
| models.json 写回 | Python dict 原地改、保留既有排版 | Go：未动子树原字节直通；被 merge provider canonical 字段序（首触一次机械 diff 后稳定） |
| 隐式行为 | 多处（env 代理、re.match 锚定、静默降级等） | 全部改显式，清单见 ARCHITECTURE §10.3，作为长期设计原则 |

## 影响面

- 新增 `server-go/`（独立 go module，不影响根 package.json）；`public/index.html` 加订阅输入与失败渲染；`server/` 内 fetcher.py/nodepool.py/app.py 少量改动（显式化对齐，不改外部契约）。
- 数据格式不变：`models.json`/`config.json` 两实现互相兼容读写。
- API 契约不变（§4），`/api/nodes` 语义变化见 §10.2。

## 回滚

`git revert` 本分支合并提交；删除 `server-go/` 即回到纯 Python 形态。Python 侧改动均为显式化对齐，可独立保留。

## 需重审门

无（纯后端实现 + 部署形态，不改产品基线交互；前端新增一个输入框与一个错误展示样式）。

## 验证记录

- `go vet` / `go build` 干净；`go test ./internal/fetch` 7 个解析器 vs Python 基准字节级一致。
- 端到端实测（Windows 开发机）：直连成功合并（bigmodel-cn 33/deepseek 2）；xai 直连超时 → 节点池 → 假订阅不可达 → report 正确含 error；未动 provider 写回 diff 为零。
- 未验证项：真实订阅 URL + 真实节点走代理（无可用订阅测试）；Linux 目标机实跑。
