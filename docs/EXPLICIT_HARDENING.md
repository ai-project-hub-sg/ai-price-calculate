# 隐式行为显式化专题报告

版本：v1.0 · 日期：2026-11-24 · 范围：`server/`（Python）与 `server-go/`（Go）全部已识别隐式行为

## 原则

> **两个实现都不允许隐式行为——隐式行为是长期维护的 bug 源。**
> 凡行为依赖「恰好如此」的默认值、语言惯例、环境状态、隐式锚定、静默容错，一律改为显式：显式参数、显式返回、显式日志、显式报错。
> 本文件是判定基线；新增隐式行为需在评审中指出并改显式，同时登记到本文件。

## 登记清单

| # | 隐式行为 | 位置 | 显式化方式 | 守护测试 |
|---|---|---|---|---|
| 1 | **环境变量代理**：Python `urllib.build_opener`/`urlopen` 与 Go `http.ProxyFromEnvironment` 静默读 `HTTPS_PROXY`——「直连」可能在不知情时走代理 | `server/nodepool.py`、`server/app.py`、`proxypool/pool.go` | Python 建 `ProxyHandler({})` 空代理 opener 严格直连，`app._fx` 改走它；Go `Transport{Proxy:nil}`。代理仅经显式订阅配置 | — |
| 2 | **`re.match` 隐式行首锚定** vs Go `MatchString` 全局搜索——逐字移植正则会在 Go 侧匹配到行中子串 | `server/fetcher.py` 解析器（3 处）、`providers.go` | 两边统一 `re.search`/`MatchString` + 字面量内显式 `^` 与 `(?i)`，**正则字面量逐字一致** | `parse_test.go` 7 项 |
| 3 | **订阅体 base64 嗅探**：多种编码逐个猜，猜对不吱声 | `subscription.go` | 显式逐编码尝试（std→raw-std→urlsafe→明文），`how` 返回值并在 job 日志报告实际命中 | `subscription_test.go::TestDecodeSubscriptionVariants` |
| 4 | **`len(body)>500` 魔法数**（判别 Kimi 等 JS 渲染页返回的空壳 .md） | `fetcher.py`、`update.go` | 命名常量 `MIN_USEFUL_BODY`/`minPricingBytes` + 注释「显式阈值，勿静默改」 | — |
| 5 | **merge 的 truthy 语义**：`if row.get("status")` 把「字段缺失」与「空字符串」混为一谈 | `fetcher.py`（已加显式注释）、`update.go` | Go 显式判 `ok && s != ""`；Python 注释说明仅非空字符串覆盖 | `merge_test.go` |
| 6 | **`models.json.bak` 备份失败被忽略**，随后仍覆盖原文件 | `update.go` | 备份失败 = job 失败，禁止无备份覆盖（对齐 Python `copy2` 未 guard 的行为） | — |
| 7 | **请求体含未知 provider id**：Python 抛 KeyError 崩 job；Go 曾静默跳过 | `update.go` | 显式 log + report `{error:"未知 provider id"}` | — |
| 8 | **POST body 非法 JSON 静默当 `{}`** | `httpsrv/server.go` | 非空 body 解析失败 → 400 | — |
| 9 | **config.json/models.json 损坏静默返回默认/空**——下次 PUT 无声覆盖损坏文件 | `httpsrv/server.go`（Python 已先行对齐） | 解析失败 → 500 + 日志 | — |
| 10 | **静态资源来源自动探测**：`./public` 存在用磁盘否则内嵌——换 cwd 即换 UI | `cmd/server/main.go` | 默认 = embed；磁盘仅 `--public` 显式指定，启动日志打印实际来源 | — |
| 11 | **`--data ./data` 相对 cwd**——换目录启动凭空建新数据目录 | `cmd/server/main.go` | 默认 = 可执行文件同目录 `data/`；flag 可覆盖；启动日志打印绝对路径 | — |
| 12 | **vless/hy2 URI userinfo 百分号编码未解码**（hy2 密码、`pbk` 等可能含 `%XX`） | `subscription.go` | 显式 `url.QueryUnescape` | `subscription_test.go::TestParseHY2` |
| 13 | **Python `app._fx` 用 `urlopen`**——除 fetcher 外第二个静默 env 代理点 | `server/app.py` | 改走 `nodepool.fetch_url`（严格直连 opener） | — |
| 14 | **订阅拉取的通路**：代理订阅本身若走代理 = 鸡生蛋 | `subscription.go` | 显式注释声明：订阅固定直连，不可达则明确报错 | — |
| 15 | **Go nil slice → JSON `null`** vs Python 空 list → `[]`：merge 报告字段形状不一致（**测试捕获的真 bug**） | `update.go::MergeProvider` | 四个列表字段 `[]string{}` 初始化 | `merge_test.go::TestMergeParityWithPython` |
| 16 | **换行符**：Python `open("w")` 在 Windows 产出 CRLF、Linux 产出 LF；Go 恒 LF 会翻转整文件行尾 | `marshal.go` | `LoadDataset` 检测输入换行风格并保留（eol 字段），canonical 块插入前统一转换 | `merge_test.go::TestMarshalUntouchedByteIdentical`（CRLF fixture 逐字节往返） |
| 17 | **JSON 数字类型丢失**：Go `encoding/json` 全解 float64，int/float 区分丢失——只看值的格式化数学上必然错一侧（`64000`→`64000.0` 或 `2.0`→`2`） | `marshal.go` | 类型保留解码（UseNumber 按 Python 词法分流 int64/float64）+ `pyFloatRepr` 按 CPython repr 写回（极端值科学计数法与 Python 一致而非禁用） | `marshal_test.go::TestMarshalCanonicalNumbers`（CPython 实测向量）、`merge_test.go::TestMergeWritebackByteParityWithPython`（逐字节） |
| 18 | **JSON 字符串隐式转义**：Go `json.Marshal` 做 HTML 转义（`<`→`\u003c`）与 Python `ensure_ascii=False` 不一致 | `marshal.go` | 手写 `writeJSONString`：仅转义引号/反斜杠/控制字符，UTF-8 原文 | `marshal_test.go::TestWriteJSONStringUTF8` |
| 19 | **map 键序**：Go `map` 序列化键序不确定；Python 保留插入序而各解析器 tier 键序异质（deepseek `input/cacheRead/output` vs xai `input/output/cacheRead`），异质是参考实现本性 | `marshal.go`、`fetcher.py::canonicalize` | 双端统一 canonical 键序（CR-004）；Go 未动子树 raw 字节直通、脏 provider 按 `fieldOrders` canonical 序，未知键排尾 | `merge_test.go::TestMergeWritebackByteParityWithPython`（逐字节对拍）、`marshal_test.go::TestCanonicalKeyOrder`、`test_fetcher.py::TestCanonicalize` |
| 20 | **`freePort` TOCTOU**（选完端口到 sing-box 监听之间存在被抢窗口） | `proxypool/pool.go` | 显式注释声明取舍（本地工具概率极低） | — |

## 一致性测试矩阵（守护「两边一致且可靠」）

| 测试 | 位置 | 保证 |
|---|---|---|
| `TestParsersMatchPython`（7 源） | `internal/fetch/parse_test.go` | Go 解析器输出与 Python 对同一 .md 的输出 JSON 归一化后深度一致 |
| `TestMergeParityWithPython` | `internal/fetch/merge_test.go` | merge 报告逐字段一致 + 合并后 provider 深度一致 + 未动子树原字节直通 |
| `TestMarshalUntouchedByteIdentical` | `internal/fetch/merge_test.go` | 无 merge 时写回与输入逐字节一致（含 CRLF 保留） |
| `TestMarshalCanonicalNumbers` 等 3 项 | `internal/fetch/marshal_test.go` | 数字格式/键序/字符串转义对齐 Python `json.dump` |
| `TestDecodeSubscriptionVariants` 等 5 项 | `internal/proxypool/subscription_test.go` | base64 变体解码、vless/hy2 URI→outbound 字段、拒绝路径、端到端加载 |

**基准再生成**：`python server-go/tools/gen_fixtures.py`（仓库根执行；固定 `TODAY=2026-11-24` 保证确定性）。解析器或合并语义改动后必须重跑并人工核对 diff。

## 遗留的已知隐式点（有意保留，勿扩散）

- `GET /api/config` 在文件不存在时返回内置默认配置——API 契约本身（前端依赖），但**文件损坏时仍 500**。
- `--host` 默认 `127.0.0.1`：与 Python 一致的本地优先默认值，VPS 反代无需放开。
