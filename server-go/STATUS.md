# Go 版移植 — 会话状态与坑记录

> 用途：跨会话交接。每次动 `server-go/` 前先读本文件 + `docs/DUAL_IMPLEMENTATION.md`。
> 最近更新：2026-11-24（feat/go-port，3 个提交，未 push）

## 当前状态

- 分支 `feat/go-port`：`f8250f0`（主体移植）、`9869c5e`（一致性测试+文档）、再加节点测试 UX 批次
- 全部 `go test ./...` 绿：7 解析器 vs Python 基准、merge 三分支 parity、写回零 diff 往返、订阅解析 5 项
- 实测通过：直连合并、订阅拉取（真实 37 节点订阅）、节点池探测、测试端点端到端
- 运行中的实例：`aipc-server.exe --port 8787 --data ../data`（embed 前端已含订阅区按钮）

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

### 5. 前端轮询报「not found」（未定论）

用户实测价目更新：直连阶段日志正常，随后报 `✗ 失败: not found`。排查结论：`j.status` 非 running/done 且 `j.error='not found'` = 轮询拿到 `/api/jobs/<id>` 的 404。本地复现同一订阅同一 job → `status:done` 正常完成，未复现。已加 API 请求日志（`/api/*` → stderr 打 `方法 路径 → 状态码`），**若再现先看服务端 stdout 轨迹**（是否请求落到不含该 job 的实例/是否请求路径异常）。怀疑方向：瞬时因素（实例重启竞态/浏览器预览代理），无代码级证据前不改逻辑。

## 待办 / 未验证

- [ ] 用户重测：保存按钮反馈、测试节点按钮（15/25 可用结果已知）、价目更新完整链路
- [ ] 「not found」若再现 → 拿服务端日志定位
- [ ] Linux 交叉编译产物在目标机实跑（`GOOS=linux ... -tags`）
- [ ] push `feat/go-port` 需当次授权（触发署名/作者检查技能）
- [ ] 节点探测 `maxNodes=25` 默认值与前端展示（37 节点只测 25）是否需要暴露给用户配

## 环境

- `server-go/aipc-server.exe`（54.7MB，含 utls+quic tags）在仓库外 .gitignore
- `data/config.json` 已含用户真实 `proxySubscription`（gitignore，勿提交）
- `data/models.json` 曾被测试更新改过——已 `git checkout` 恢复；再测试会再次产生写回
