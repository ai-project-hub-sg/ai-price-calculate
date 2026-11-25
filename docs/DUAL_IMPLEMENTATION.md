# 双实现维护指南（Python ↔ Go）

> **每次涉及 `server/` 或 `server-go/` 的开发前必读本文件。**
> 本项目同时维护两个后端：Python（`server/`，开发参考实现）与 Go（`server-go/`，部署目标）。
> 它们共享 `public/` 前端、`data/*.json` 数据格式与 §4 API 契约，但**不是逐行翻译关系**——维护规则如下。

## 1. 什么时候必须双改 / 什么时候只改一边

| 改动类型 | 要求 |
|---|---|
| API 契约（路径、方法、请求/响应形状、错误格式） | **双改 + 同步 ARCHITECTURE §4** |
| 价目解析逻辑（正则、表结构、字段映射） | **双改 + 重跑 `gen_fixtures.py`**，正则字面量必须逐字一致 |
| merge/写回语义（字段覆盖规则、日期戳、备份） | **双改 + 重跑 `gen_fixtures.py`** |
| config.json 顶层字段（如 `proxySubscription`） | **双改**：两边都从顶层读 |
| 节点获取机制 | 单改各自一侧——这是**有意分歧点**（v2rayN vs 订阅+内嵌 sing-box） |
| UI 交互、calc.js | 单改 `public/`——两个后端共用 |
| 仅部署形态（flags、embed、systemd） | 单改 Go |

规则：**改完一边时，必须检查本文件第 2 节「等价性清单」判定另一边是否要跟进；不需要跟进则在 PR 说明理由。**

## 2. 等价性清单（改任何一边时逐项核对）

两实现必须**行为等价**的契约面（不等价即 bug，须有测试或文档解释）：

1. **HTTP 端点集合与响应形状**：`/api/models` `/api/config`(GET/PUT) `/api/fx` `/api/nodes` `/api/nodes/test`(POST) `/api/pricing/update` `/api/jobs/<id>` `/api/shutdown`(POST)；错误 `{error: "..."}` 字段名一致。job 对象含 `progress`（结构化进度：phase/item/itemIndex/itemTotal/targets/done/current/startedAt——`done`=已解决厂家 id 列表，`current`=在飞厂家 id 集合）与 `startedAt`（job 创建 epoch ms）——两边都产出，前端进度卡依赖此契约。
2. **价目解析**：每个 SOURCES 源对同一 .md 输入产出相同行集（id/status/contextWindow/tiers/notes/officialUpdatedAt）。
3. **合并语义**：updated/added/skippedRetired/missingInSource 四列表含义与空值形状（`[]` 不是 `null`）；`retired` 不入库；新增行补 `status=active`/`fetchedAt`/`officialUpdatedAt=null`/`notes=自动更新新增，请人工核对`；`checkedAt` 必写。
4. **models.json 写回**：缩进 2 空格、UTF-8 原文不转义、数字十进制无科学计数法、保留文件换行风格、**未动数据零 diff**。
5. **config.json**：不存在→默认（API 契约）；损坏→500；PUT 要求 JSON 对象、≤2MB、校验非空 proxySubscription 的 http/https 格式，并发写加锁并原子写入；其余 schema 由前端 validateConfig 负责。
6. **鉴权**：login/logout 契约一致；除登录、OPTIONS、静态资源外全部 API 要求 Bearer token；12h TTL、5 次失败/15min；CORS 均为 `*`；凭据来源按平台约定，无默认密码。
7. **回退链**：直连 → 节点池（有配置时）→ 报错；`bytes > MIN_USEFUL_BODY(500)` 且**该源解析器能解出非空行**才视为抓到（直连与节点胜出同标准）；胜出者复用探测时校验过的响应体，不二次抓取；订阅/直连均不走环境变量代理。
8. **fetch URL 集合**：`SOURCES`/`Sources` 的 8 个 URL、顺序（report/unreachable 排序稳定性）、moonshot 无解析器→manual-check。
9. **后台任务**：价目更新串行化；job 响应在锁内快照；shutdown 拒绝运行中任务，信号停机先拒绝新任务再等待；Go 业务 goroutine panic 转为 job error。

## 3. 已知坑（踩过的，防再踩）

| 坑 | 根因 | 对策 |
|---|---|---|
| 正则行为漂移 | Python `re.match` 隐式行首锚定 ≠ Go `MatchString` 全局搜索 | 字面量统一 `re.search`/`MatchString` + 显式 `^`、`(?i)`；逐字一致 |
| 静默走代理 | urllib/http.DefaultTransport 都读 `HTTPS_PROXY` | 两边都用显式空代理 Transport；新增任何 HTTP 客户端代码同样处理 |
| `null` vs `[]` | Go nil slice → `null`，Python `[]` | 列表字段永远 `[]string{}` 初始化，不依赖零值 |
| 数字变科学计数法 | Go `fmt` 大数格式化、`encoding/json` float64 策略 | 自写 canonical marshal：数字走 `json.Marshal` 十进制 |
| HTML 转义污染 | Go `json.Marshal` 默认转义 `<>&` | `writeJSONString` 手写（对齐 `ensure_ascii=False`） |
| 键序漂移 | Python dict 保序 vs Go map 乱序 | raw 直通 + dirty 子树 canonical；见 ARCHITECTURE §10.4 |
| 换行翻转 | Python `open("w")` 按 OS 写 CRLF/LF | `Dataset.eol` 检测并保留输入换行风格 |
| fixture 过期 | .md 样本是某时刻快照 | 源页改版→更新 fixture + 重跑 gen_fixtures.py；真实源结构变了≠测试错 |
| 200 墙页当成功 | 地区限制/反爬页也是 200+大字节（实测 anthropic 311KB blocked 页） | 直连与节点胜出都做「解析非空」校验；节点胜出复用探测 body（重抓有 CDN 抖动窗口） |
| v2rayN DB schema 漂移 | v7 把 VLESS UUID 移到 `Password` 列（`Id` 为 NULL）、flow/encryption 挪进 `ProtoExtra`、transport 细节挪进 `TransportExtra` | `_xray_vless_config` 全部新旧兼容读取（`Id`→`Password`、`Flow`→`ProtoExtra.Flow`、`HeaderType`→`TransportExtra.RawHeaderType`）；xray 启动失败要能看到 stderr（勿 DEVNULL 吞掉） |
| urllib per-op 超时累计 | `timeout` 只管单次 socket 操作，DNS/多 IP 顺序重试累计可达 70s | `fetch_url` 用 daemon 线程包硬上限 `timeout*1.5+2` |

## 4. 测试基准的再生工作流

```
# 仓库根执行。所有 *.expected.json / merge_*.json 由 Python 参考实现产出。
python server-go/tools/gen_fixtures.py
```

- 固定 `TODAY=2026-11-24` → 确定性输出，diff 可直接进 git review。
- **改解析器 → 重跑 → 人工核对 expected diff 是否合理**（expected 是基准不是真理，解析器有意变更时基准随之更新）。
- testdata/*.md 来自 `tmp_fetch/` 真实抓取样本；源页面结构变化时同步更新 .md 与基准。

## 5. 有意分歧点（不要"对齐"）

- **`/api/nodes` 节点来源**：Python=v2rayN sqlite；Go=订阅 URI。字段形态保持兼容（id/名称/类型/延迟或地址），但来源与数量天然不同。
- **节点池实现**：Python 拉子进程（xray/sing-box 二进制）；Go 进程内嵌 sing-box 库。探测并发度、超时值各自实现内可调，不追求一致。
- **节点源不同**：Python=本地 v2rayN sqlite（`guiNDB.db`）；Go=代理订阅 URL。因此「订阅列表缓存/拉取重试/过期兜底」只有 Go 侧——结构性分歧，不对齐。前端订阅输入框在 Python 后端下无实际作用。
- **节点可用性缓存**（两边都有，实现各自）：Go=`proxypool.Cache`（outbound 序列化为键）；Python=`nodepool._NODE_CACHE`（index_id 为键）。同为进程内 1h TTL：可用节点温路径复用、「测试节点」结果快照秒回（`force`/`强制重测` 绕过）、探测结果回写、缓存节点失效即剔除。
- **启动参数**：Go 有 `--host/--data/--public`（部署需要）；Python 只有 `--port`（开发机约定 `data/`、`public/` 相对仓库根）。
- **凭据平台实现**：两端均优先读完整凭据环境变量；Windows 缺省时读 `aipc-server/admin`。Python 用标准库 ctypes，Go 用 Windows build-tag 文件；错误均 fail closed。

## 6. 提交前检查单

- [ ] 行为变更两边都改？或本 PR 说明为何单边？
- [ ] `go test ./server-go/...` 全绿（含基准比对）
- [ ] 改了正则/merge/解析 → `gen_fixtures.py` 已重跑且 diff 人工看过
- [ ] 新隐式行为？→ 改显式并登记 `docs/EXPLICIT_HARDENING.md`
- [ ] API/数据格式变了 → `docs/ARCHITECTURE.md` §4/§10 与本文件 §2 同步
