# Go 版移植 — 会话状态与坑记录

> 用途：跨会话交接。每次动 `server-go/` 前先读本文件 + `docs/DUAL_IMPLEMENTATION.md`。
> 最近更新：2026-11-25（fix/go-port-review-fixes，CR-003 + CR-004 待用户核验）

## 当前状态

- `feat/go-port` 是审查基线；本轮修复位于独立分支 `fix/go-port-review-fixes`（基线 `83b6c06`），**未合并、未申请 PR**。
- CR-003 覆盖配置保存错误、订阅凭证脱敏、严格 job 参数、FX 缓存/刷新、可执行文件数据目录、HTTP 超时、PBKDF2 锁粒度、失败记录清理、节点 panic 恢复、Transport 清理、URI/Base64 解析、merge 顺序与文件权限。
- CR-004 补完：代理胜出字节门槛内嵌 validate（温/冷/直连同标准，Python 同修）；`maxNodes` 无魔法值（缺省 999 等同全部，正整数值，`0`/负数/`25.1` 报 400）；数字写回改类型保留解码 + CPython repr；双端统一 canonical 键序 + 逐字节对拍守门；`data/models.json` 已一次性键序归一化（纯顺序零内容变化）。
- 所有后端 API（除登录、OPTIONS、静态资源）需 token；前端只存 sessionStorage。CORS 按用户要求保持 `*`。
- 启动必须配置 `AIPC_ADMIN_USERNAME`/`AIPC_ADMIN_PASSWORD`；Windows 两者均未设置时读取凭据管理器 `aipc-server/admin`。无默认密码。
- Python 侧同步鉴权、原子写、job 深拷贝与信号停机；节点来源仍与 Go 有意不同。
- 原功能层：①节点可用性缓存（1h TTL）②订阅节点列表缓存/重试/过期副本兜底 ③结构化实时进度 ④节点测试端点 ⑤ shutdown 端点。
- 历史运行记录：Go `aipc-server.exe --port 8787 --data ../data`、Python `python -m server.app --port 8788`；旧的无鉴权启动命令已不适用。

## 已踩的坑（防再踩）

### 1. sing-box build tags — 最大雷

默认 `go build` 编译出的库**出站协议不完整**：Reality 要 `with_utls`，Hysteria2 要 `with_quic`，缺失时 `box.New` 成功但 `initialize outbound` 报 `not included in this build`——**所有节点探测全灭但进程不崩**。症状是「订阅能解析、节点一个不能用」。所有构建命令必须带 `-tags "with_utls with_quic"`（README/DEPLOYMENT/AGENTS.md 已更正）。教训：spike 只验证了监听端口，没验证协议出站——**库嵌入验证要走到真路径**。

### 2. `*.exe~` 误提交与 .git 体积

Windows 下 `go build -o aipc-server.exe` 覆盖**正在运行的** exe 时，链接器把旧文件改名 `aipc-server.exe~` 再写新的 → `git add -A` 把 49MB 残留提交。处理过程与最终结论：

- `git rm --cached` + `commit --amend` 只让**新提交树**不含该文件；旧提交仍在 HEAD reflog 里可达 → **blob 留在对象库，.git 体积不变**（实测 51MB→不变）
- 真删要：`git reflog expire --expire=now --all && git gc --prune=now` → .git 51MB→447KB，`cat-file` 验证旧提交彻底消失
- 前提：分支未 push（无远端副本）；rebase/amend 类操作后大文件同理要 gc 才真消失
- 已防：`server-go/.gitignore` 加 `*.exe~`；**提交前 `git status` 检查 `*~` 与异常大文件**

### 3. sing-box 无 dns 段 → DNS 协程 panic 崩全进程（已修）

症状：节点测试到 ~8 个节点时整个服务进程消失（`connection refused`）。栈：`dns.Client.newCacheKey` nil 解引用，panic 在 sing-box `task.Group` 自有 goroutine —— **业务层 recover 兜不住**（不在调用栈内）。根因：box 配置没给 `dns` 段，v1.14 新 DNS 实现并发交换时踩未初始化字段。修法：`StartNode` 每个 box 显式 `dns.servers=[{type:local}] + final=local`（系统解析器，探测语义也正确）。教训：**嵌入第三方库的关键路径（出站协议、DNS、路由）都要显式配置，不依赖库缺省**——与显式原则一致，已登记进 EXPLICIT_HARDENING 思路。

### 4. 前端对空 dataset 无兜底（已修）

`models.json` 缺失时 `/api/models` 返回 `{}` → `state.dataset.providers` undefined → `is not iterable` 全页面崩。修：boot 校验 `providers` 是数组才采用，否则回退内置样本 + banner 明示「models.json 缺失/为空」。注意 exe 默认 `--data <exe>/data` 与仓库 `data/` 是两处——开发测试传 `--data ../data`。

### 5. 「200+大字节=抓到」被墙页骗过（双边逻辑洞，已修）

anthropic 直连 307→`app-unavailable-in-region` 311271B 落地页：200、>500B、解析 0 行，旧逻辑记 `parse-empty` 不重试。三连修：①直连后**循环内**预解析（统一后置检查会让 blocked 页在 direct 阶段假绿）；②节点胜出校验加「该源解析器解出非空行」；③胜出者复用探测时校验过的 body——**重抓有 TOCTOU 抖动**（探测真页/重抓墙页实测发生）。Python 同步。根因教训：**「内容有效」要用领域校验（解析器），不要用传输层信号（status/字节数）推断。**

### 6. 进度显示的并发/时序坑（已修，UX 三条）

- `progress.current` 单值被 4 个并发节点互相覆盖 + 快完成被 1.5s 轮询错过 → 改**在飞集合**（OnTarget/OnTargetDone 加减），且 emit 须在加入**之后**调用才蓝
- Go map/set 并发写：inflight 从探测 goroutine 改 → Go 用 `sync.Mutex`、Python 用 `threading.Lock` + `bump()`，读快照也要拿锁（set changed size during iteration）
- 厂家被解决瞬间无 emit → done 只在下一事件捎带出现，最后一个解决完即 merge→done→卡片消失，绿色从没被采到 → `OnWin`/absorb 胜出瞬间 emit + doneSet 即时置位 + **前端 done 后定格终态卡**（✓绿/✗红）
- Go-only bug：冷路径胜出者删的是 FindWorking 内部 pending 副本，外层 needProxy 没动 → 经节点成功的厂家被误列 unreachable（Python 用 probe["pending"] 无此病）

### 7. v2rayN 7.x DB schema 漂移（Python-only，已修）

`ConfigVersion=4` 的库：VLESS UUID 移到 `Password` 列（`Id`=NULL → xray `invalid UUID:` 启动即崩）、flow 挪进 `ProtoExtra` JSON、transport 细节挪进 `TransportExtra` JSON。症状：全部节点「本地端口未就绪」秒灭，跟 v2rayN 开没开无关（独立拉子进程）。修：`_xray_vless_config` 全部新旧兼容读取；`_LocalProxy` 失败时吐 xray stderr 尾行（之前 DEVNULL 吞掉真错）+ exe 存在性预检。教训：**第三方数据源的 schema 要做版本兼容读取；子进程失败必须能看到它的 stderr**。

### 8. urllib per-op 超时累计（Python，已修）

`fetch_url(timeout=12)` 的 timeout 只管单次 socket 操作——DNS + 多 IP 顺序重试每个吃满，google 直连实测卡 70s。修：daemon 线程包**总耗时硬上限** `timeout*1.5+2`（线程杀不死，超时放弃，代价可接受）。对照：Go 的 http.Client.Timeout 天然是全程上限，无此病。

### 9. 前端轮询报「not found」（未定论）

用户实测价目更新：直连阶段日志正常，随后报 `✗ 失败: not found`。排查结论：`j.status` 非 running/done 且 `j.error='not found'` = 轮询拿到 `/api/jobs/<id>` 的 404。本地复现同一订阅同一 job → `status:done` 正常完成，未复现。已加 API 请求日志（`/api/*` → stderr 打 `方法 路径 → 状态码`），**若再现先看服务端 stdout 轨迹**（是否请求落到不含该 job 的实例/是否请求路径异常）。怀疑方向：瞬时因素（实例重启竞态/浏览器预览代理），无代码级证据前不改逻辑。

## 待办 / 未验证

- [x] 历史用户重测：订阅区按钮、测试节点、价目更新链路、进度显示、关闭服务（无鉴权基线）
- [x] 历史 push `feat/go-port`（既有授权，15 提交全 `notebook` 身份、0 AI 署名、无二进制被追踪）
- [x] 前序 `fix/go-port-hardening` 离线单测/race/vet/tags/Linux+Windows 交叉编译、syncassets 与未登录浏览器检查
- [x] 隔离实例鉴权 API 冒烟；登录后完整 UI 流程仍待用户人工核验
- [ ] Windows Credential Manager 真实读取；Linux 目标机 systemd/nginx 与真实订阅端到端
- [ ] 用户核验 `fix/go-port-review-fixes` 后由用户合并 `feat/go-port`；本轮不执行 merge
- [ ] 「not found」若再现 → 拿服务端日志定位

## 环境

- `server-go/aipc-server.exe`（~55MB，含 utls+quic tags）在 .gitignore
- `data/config.json` 已含用户真实 `proxySubscription`（gitignore，勿提交）
- `data/models.json` 被测试更新改过属正常数据产物；提交代码前 `git checkout -- data/models.json` 恢复
- Python 版节点源 = `%LOCALAPPDATA%\v2rayN\guiConfigs\guiNDB.db`（ConfigVersion=4，v7 schema）
