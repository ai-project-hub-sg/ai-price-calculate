# CR-003 go-port 审查修复：凭证保护、API 契约与代理等价性

- 状态：**approved for implementation**（用户于 2026-11-25 要求从最新 `go-port` 开独立分支、修复审查问题、提交 CI；**未授权合并**）
- 日期：2026-11-25
- 基线：`origin/feat/go-port@83b6c06556ab37c58a392bebfc2f540247844a3e`
- 修复分支：`fix/go-port-review-fixes`
- 作者身份：仓库本地 `ai-project-hub-sg <ai-project-hub@outlook.com>`，与 GitHub 提交关联账号一致；全局 Git 配置不变。

## 目标

修复独立审查发现的高优先级缺陷和双实现等价性问题，使候选版本可以进入正式人工验收。所有变更只进入修复分支并推送触发 CI，不合并 `feat/go-port` 或 `main`。

## 非目标

- 不读取用户真实密码、真实订阅或真实运行配置。
- 不改变产品层级、计算口径、数据格式或部署形态。
- 不引入新的前端框架、Go/Python 依赖或外部服务。
- 不以本 CR 代替人工 UI 验收、Windows 凭据管理器实测、Linux VPS 或真实代理订阅端到端验证。

## 修复需求

### A. 数据完整性与凭证保护

| ID | 优先级 | 问题 | 期望行为 |
|---|---|---|---|
| A1 | 高 | 前端普通配置保存忽略 HTTP 400/500，用户误以为保存成功 | 非 2xx 必须显示明确错误；localStorage 与后端不一致时不得静默成功 |
| A2 | 高 | HTTP 错误包含订阅 URL 查询令牌，并进入 job 日志/报告 | 错误与日志不得包含订阅 URL 的 query、userinfo 或原始凭证；只保留脱敏后的主机信息 |
| A3 | 中 | 非法 `providers` 退化为更新全部源，非法 `maxNodes` 被静默忽略 | 字段存在时必须严格校验；错误返回 400，不得启动 job |
| A4 | 中 | 汇率上游返回 200 但结构错误时缓存 `rate:null` 24 小时 | `rates.CNY` 必须为有限正数；无效响应返回 502 且不写缓存，已有无效缓存不得复用 |
| A5 | 中 | 默认数据目录基于 `os.Args[0]`，PATH 启动时可能指向错误目录 | 默认目录必须基于 `os.Executable()`，启动日志打印绝对路径 |

### B. 鉴权与 HTTP 加固

| ID | 优先级 | 问题 | 期望行为 |
|---|---|---|---|
| B1 | 中 | 过期登录失败记录只清理当前客户端，其他客户端永久累积 | 每次登录/鉴权/退出时全局清理过期失败记录，并补跨客户端测试 |
| B2 | 中 | 120,000 轮 PBKDF2 在全局 mutex 内执行，阻塞所有会话鉴权 | 密码摘要计算不持有会话/失败记录锁；map 操作仍保持并发安全 |
| B3 | 中 | `http.Server` 无连接超时，慢客户端可长期占用资源 | 设置合理的 ReadHeader/Read/Write/Idle 超时，不影响异步 job 和订阅加载 |
| B4 | 中 | `TestNodes` 工作 goroutine 缺少 panic 恢复，异常可终止进程 | 单节点 panic 转该节点错误，不得使整个服务退出 |

### C. 订阅解析与代理抓取

| ID | 优先级 | 问题 | 期望行为 |
|---|---|---|---|
| C1 | 中 | 每次 `FetchURL` 新建 Transport 且不关闭 idle 连接 | 请求结束后关闭该 Transport 的 idle 连接，避免连接/goroutine 累积 |
| C2 | 中 | 代理胜出响应绕过 500 字节门槛，可能显示已解决但未合并 | 直连与代理路径统一执行最小正文门槛；低于门槛不得胜出或标记 resolved |
| C3 | 中 | 温缓存路径忽略 `MaxNodes` | 所有路径均遵守 `MaxNodes`，冷/温路径语义一致 |
| C4 | 中 | VLESS/HY2 userinfo 已由 `url.Parse` 解码后再次 `QueryUnescape`，`+` 被错误转成空格 | 只解码一次，保留合法 `+` 与百分号文本 |
| C5 | 中 | 节点 URI 未校验 host、凭证和端口上限，非法节点挤占有效候选 | 拒绝空 host/凭证、非 Reality VLESS、端口越界及必要字段缺失的节点 |
| C6 | 中 | 不支持带 padding 的 URL-safe Base64 订阅 | 支持 standard padded、URL-safe padded、standard raw、URL-safe raw 和明文 |
| C7 | 低 | `missingInSource` 来自 Go map，顺序不稳定 | 按数据集原模型顺序输出，与 Python 一致 |
| C8 | 低 | 写回固定 0644，破坏原文件权限 | 写回保留原 `models.json` 权限；备份沿用源权限 |
| C9 | 低 | 小手数字仍可能输出科学计数法 | 所有 canonical JSON 数字使用十进制表示 |
| C10 | 低 | xAI 解析在上下文无法识别时省略 `contextWindow` | 与 Python 一致始终输出该字段，无法识别时为 `null` |

### D. 既有契约与文档

| ID | 优先级 | 问题 | 期望行为 |
|---|---|---|---|
| D1 | 低 | 手动刷新汇率没有传 `refresh=1`，两端也忽略该参数 | 手动刷新绕过 24 小时缓存；自动启动仍使用缓存 |
| D2 | 低 | Python/JS URL 长度按字符计，Go 按 UTF-8 字节计 | 三端统一为 4096 UTF-8 字节，并补 Unicode 测试 |
| D3 | 低 | `server-go/STATUS.md`、测试计划、开发文档和二进制大小说明存在过期内容 | 文档与本分支真实行为、测试入口和验证状态一致 |

## 修复方案

1. **补回归测试**：为 A2–A5、B1–B4、C1、C3–C10、D1–D2 添加可复现测试；A1 由严格 API 封装与前端语法检查覆盖，登录后 UI 可见错误仍须人工验收。测试覆盖直连/代理回退、正文门槛、温路径限制、凭证脱敏、备份失败、并发更新和 Transport 清理。
2. **前端与共享校验**：改用严格 JSON API 封装处理保存错误；手动汇率刷新传 `refresh=1`；URL 长度改为 UTF-8 字节。
3. **Go HTTP/鉴权**：严格解析 job 请求；校验 FX 响应与缓存；改用 `os.Executable`；补 HTTP 超时；将 PBKDF2 移出锁临界区并全局清理失败记录。
4. **Go 代理/抓取**：集中脱敏网络错误；关闭 idle 连接；统一正文门槛与 `MaxNodes`；修复 URI 解码和字段校验；补 URL-safe Base64；保留文件权限与稳定顺序；修复数字和 xAI 字段。
5. **Python 对拍**：同步汇率刷新参数和 URL 字节限制，保持双端契约；Go 特有代理实现不强行复制 Python 的 v2rayN 细节。
6. **文档与资产**：更新 CR、DUAL_IMPLEMENTATION、TEST_PLAN、STATUS、DEVELOPMENT 和相关 README；运行 `syncassets` 保证 embed 与 `public/` 一致。
7. **验证与交付**：运行完整测试、race、vet、构建和隔离 API 冒烟，审阅 staged diff 后提交并推送修复分支，等待 CI；不执行 merge。

## 验收标准

- 订阅令牌不会出现在错误、日志、报告或 API 响应中。
- 配置保存失败在 UI 有可见错误；成功保存后重载保持一致。
- 非法 job 参数返回 400 且不创建 job；合法空数组仍保持“全部源”语义。
- 无效汇率响应不缓存；手动刷新确实重新请求上游。
- 代理抓取所有路径遵守最小正文和 `MaxNodes`；含 `+` 的凭证、URL-safe Base64 和正常 Reality/HY2 节点可解析。
- 登录失败记录按窗口清理；PBKDF2 不阻塞已有会话鉴权；慢连接和单节点 panic 不拖垮服务。
- Go/Python URL 字节限制一致；merge 顺序、文件权限、数字格式和 xAI 字段对拍一致。
- `npm test`、Go 默认/tagged/race 测试、`go vet`、Linux/Windows tagged 构建、embed 一致性检查全部通过。
- 修复分支 CI 全绿，工作区无运行产物或密钥，提交作者为 `ai-project-hub-sg <ai-project-hub@outlook.com>`。

## 影响面与风险

- 共享前端和 Python API 会有少量契约修正，需要同步回归。
- Transport 关闭策略只影响请求复用，不改变代理选择语义。
- HTTP WriteTimeout 必须大于最长订阅加载时间，避免误伤慢订阅。
- 节点校验趋严可能使原本“可解析但不可连接”的脏订阅更早报错，这是期望行为。
- 真实订阅、Windows Credential Manager 和 Linux 部署仍须人工验证，自动测试不能替代。

## 回滚

在合并前放弃 `fix/go-port-review-fixes` 即可恢复到 `83b6c06`。若修复分支已推送但未合并，可删除远端修复分支；不得回滚或改写 `feat/go-port` 历史。合并后如需回滚，应 revert 本分支对应提交并重新跑 CI。

## 验证记录

2026-11-25 在 `fix/go-port-review-fixes` 工作树完成：

| 验证 | 结果 |
|---|---|
| `node --check`（calc/validation/demo-config）+ `public/index.html` 内联脚本解析 | 通过，1 个内联脚本块 |
| `npm test` | 通过：Node 4 项、Python unittest 45 项 |
| `python -m compileall -q server` | 通过 |
| `cd server-go && go test -count=1 ./...` | 通过 |
| `cd server-go && go vet ./...` | 通过 |
| `cd server-go && CGO_ENABLED=1 go test -race -count=1 ./...` | 通过 |
| `cd server-go && go test -count=1 -tags "with_utls with_quic" ./...` | 通过 |
| `syncassets` + `diff -ru --exclude=.gitkeep ../public assets/public` | 通过，零 diff |
| Linux/Windows amd64 tagged 构建 | 通过，约 54MB / 53MB，产物仅放临时目录 |
| 隔离 Python/Go 鉴权 API 冒烟 | 通过：静态页、401、登录、models、config 写入/回读、非法 JSON 400、退出后 401；仅测试凭据与临时数据 |
| Python 对拍 fixture 再生 | 通过，无 fixture diff |

置信度与限制：

- 自动测试覆盖双端 API 契约、鉴权、FX、job 校验、URI/Base64、脱敏、panic、Transport、merge/权限/数字格式与解析对拍。
- 未读取用户真实密码、订阅或运行配置；未执行真实价目更新或节点探测。
- 浏览器连接器此前不可用，且不得脚本化真实凭据，因此登录后 UI 全流程、Windows Credential Manager、Linux VPS 与真实订阅仍须人工验收。
- 修复代码提交：`1d33583efbbe8ee3d5718f2a48ab6511ef35936e`。推送因本机 GitHub 凭据对话框被取消且无非交互凭据而未完成；CI run 待实际推送后补充，不得以历史绿色 CI 代替本次结果。
