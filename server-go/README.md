# aipc-server — Go 版后端

AI 价格归一化计算器的**单二进制后端**：与 Python 版（`../server/`，参考实现）API 契约一致，目标场景是 Linux VPS + nginx 反代部署——免装 Python/Node，一个可执行文件 + 一个 `data/` 目录即可运行。

- 前端 `public/` 已 `embed` 进二进制，无需拷贝静态文件
- 官方价目更新：8 源直连抓取 → 失败时经**用户配置的代理订阅**（进程内嵌 sing-box，支持 vless+reality / hysteria2）→ 报错
- 仅一个外部依赖：`github.com/sagernet/sing-box`（作为库内嵌，不是外部进程）
- `CGO_ENABLED=0`，纯静态二进制，交叉编译零障碍

## 快速开始

```bash
# Windows / Linux / macOS 通用（需要 Go ≥ 1.24）
cd server-go
go run ./tools/syncassets   # 把 ../public 同步进 assets/（embed 前置步骤）
go build -o aipc-server ./cmd/server
./aipc-server               # → http://127.0.0.1:8787
```

打开 `http://127.0.0.1:8787/`。数据文件默认读写 **可执行文件同目录下的 `data/`**（`models.json` 必须放入，`config.json`/`fx-cache.json` 运行时自动生成）。

### 命令行参数

| 参数 | 默认 | 说明 |
|---|---|---|
| `--host` | `127.0.0.1` | 监听地址；nginx 同机反代保持默认即可 |
| `--port` | `8787` | 监听端口 |
| `--data` | `<exe>/data` | 数据目录（models.json/config.json/fx-cache.json） |
| `--public` | 内嵌 | 指定磁盘前端目录（开发调试用，实时生效）；缺省=embed |

启动日志会打印数据目录绝对路径与前端来源——两个隐式来源已显式化。

## 价目更新与代理订阅

「官方价目」页 → 工具栏「代理订阅」输入框填入订阅地址（vless/hysteria2 节点订阅 URL），保存后点击「⟳ 一键更新官方价目」。回退链：

```
每个源：严格直连 →（配置了订阅且直连失败）逐节点探测代理重试 → 标记不可达报错
```

- 订阅地址本身必须直连可达（鸡生蛋：它不走节点代理）
- 节点池在进程内起 sing-box 监听 `127.0.0.1` 随机端口逐节点探测，无外部进程/文件
- 支持 `vless://`（security=reality，transport: tcp/ws/grpc/httpupgrade）与 `hysteria2://`/`hy2://`（含 mport 端口跳跃、salamander obfs）

## 测试

```bash
cd server-go
go test ./...   # 解析器/合并/写回 vs Python 基准 + 订阅解析单测
```

基准由 Python 参考实现产出，改动解析/合并逻辑后需在**仓库根**重跑：

```bash
python server-go/tools/gen_fixtures.py   # 固定 TODAY，确定性输出
```

详见 `../docs/DUAL_IMPLEMENTATION.md`（双实现维护规则，改任何一边前必读）。

## 交叉编译（Linux 部署）

```bash
cd server-go
go run ./tools/syncassets
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o aipc-server ./cmd/server
```

产物 ~49MB（内嵌 sing-box）。部署步骤见 `DEPLOYMENT.md`（systemd + nginx）。

## 目录

```
cmd/server/        入口（flags）
assets/            embed 前端（内容由 tools/syncassets 生成，勿手改）
internal/fetch/    8 源解析器 + 合并 + canonical 写回（对应 fetcher.py）
internal/proxypool/ 订阅解析 + 进程内 sing-box 节点池（替代 nodepool.py）
internal/httpsrv/  REST 端点 + 静态服务 + 后台 job（对应 app.py）
tools/syncassets/  构建辅助：../public → assets/public
tools/gen_fixtures.py  测试基准再生成（跑 Python 参考实现）
```

## 与 Python 版的关系

Python 版是参考实现：解析器、merge 语义、API 契约以它为准，Go 侧有逐条一致性测试守护。有意分歧点（节点来源、启动参数等）见 `../docs/DUAL_IMPLEMENTATION.md` §5。
