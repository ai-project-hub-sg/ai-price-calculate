# CR-002 Go 移植安全加固与双后端登录鉴权

- 状态：**approved**（用户于 2026-11-25 明确批准 URL 双重校验、请求体/原子写/并发修复、简单登录与 Bearer token，并要求先开修复分支，核验后再合并 `feat/go-port`）
- 日期：2026-11-25 · 分支：`fix/go-port-hardening`
- 批准边界：不增加 CORS/Origin 校验；Linux 凭据来自配置环境；Windows 使用凭据管理器；不读取用户真实订阅或真实密码；未授权 push/PR。

## 原因

Go 移植审阅发现非法订阅 URL 可触发请求构造 panic、并发配置写互踩、非法 JSON 可覆盖配置、更新任务丢更新与 job 数据竞争；管理 API 缺少鉴权，直接部署会把配置修改、抓取和关机能力暴露给能访问端口的调用方。

## 原行为 → 新行为

| 项 | 原 | 新 |
|---|---|---|
| 订阅 URL | 前端弱校验；Go 忽略 NewRequest error 后可能 panic | `public/validation.js` 与两端服务端一致校验；错误立即提示，不发起请求 |
| 配置写入 | Python 非法 JSON 返回 200；Go 固定临时文件并发互踩 | JSON 对象/2MB 上限；非空订阅 URL 校验；写锁 + 唯一临时文件 + fsync + rename/replace |
| 价目写回 | 非原子；多个更新可交错 | 两端更新全局串行；models 与备份原子写，备份失败中止 |
| 后台 job | 快照可变、shutdown 竞态、Go panic 可逃逸 | 锁内快照；shutdown 状态锁；拒绝运行中关机；panic 转 job error |
| API 鉴权 | 全部匿名 | login/logout + Bearer；12h 会话；5 次失败/15min；仅登录/OPTIONS/静态资源免鉴权 |
| 凭据 | 无 | 环境变量；Windows 可回退 Credential Manager `aipc-server/admin`；无默认密码，缺失即拒绝启动 |
| 前端会话 | 无 | sessionStorage 保存 token；401 回登录；退出/过期清理；不写日志与 config |
| CORS | `*` | 保持 `*`，按用户要求不增加 Origin 校验；HTTPS 仍是公网部署前提 |

## 影响面

- Python `server/` 与 Go `server-go/` 同步变更，共享前端新增 `validation.js`；Go embed 资产重新生成。
- API 新增 login/logout，既有业务端点全部要求 Authorization；直接脚本调用必须带 token。
- `config.json`/`models.json` 数据格式不变；管理员密码不写入数据文件。
- 部署需配置 `AIPC_ADMIN_USERNAME` 与 `AIPC_ADMIN_PASSWORD`，或 Windows 凭据管理器目标；`.env` 不自动加载且禁止提交。
- CI 增加 Node/Python 单测、鉴权冒烟、Go test/vet/race、协议 tags、Linux/Windows 构建。

## 回滚

在合并前放弃本修复分支即可。合并后 revert 对应合并提交，并恢复原部署方式；回滚到无鉴权版本时不得继续暴露公网管理面。已有数据文件无需格式迁移。

## 需重审门

- 用户在本机核验登录、退出、非法/空订阅、配置保存、节点测试、价目更新与关机交互。
- Windows Credential Manager 真实读取；Linux VPS/systemd/nginx 与真实代理订阅端到端验证。
- 完成上述验证后，用户批准才可合并 `feat/go-port`；合并提交完成后再按流程申请 PR。

## 验证记录

- `npm test`：Node 3 项 URL 校验测试、Python 42 项 unittest 通过；Python compileall 与全部前端 JS 语法检查通过。
- Go 默认及 `with_utls with_quic` 标签全量测试通过；两配置 `go vet` 与 `CGO_ENABLED=1 go test -race` 通过。
- Linux amd64 与 Windows amd64（均含协议标签、CGO 关闭）交叉构建通过；syncassets 后源码与 embed 副本一致。
- 隔离 Go 实例 API 冒烟通过：未登录 401、登录取 token、Bearer 读取 models、非法订阅 400、logout 后 401。
- 浏览器确认未登录遮罩与“请先登录”提示可见；登录字段的人工输入边界意味着登录后完整 UI 流程仍需用户核验，控制台未捕获 JS 错误。
- 未验证项：Windows Credential Manager 真实读取、Linux VPS/systemd/nginx、真实代理订阅与节点端到端。
