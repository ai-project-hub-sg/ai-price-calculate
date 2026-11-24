# Go 版部署指南（Linux VPS + nginx 反代）

目标形态：单个静态二进制 `aipc-server` + `data/` 目录，systemd 常驻，nginx 反代 80/443。

## 1. 构建（在开发机）

```bash
cd server-go
go run ./tools/syncassets                                   # 前端 → embed
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o aipc-server ./cmd/server
file aipc-server   # 应显示 static / not stripped 均可，关键是不依赖 libc
```

`CGO_ENABLED=0` 是硬要求（sing-box 纯 Go，无 C 依赖，静态产物随便搬发行版）。

## 2. 服务器布局

```
/opt/aipc/
├─ aipc-server          # 上一步产物（scp 上传）
└─ data/
   └─ models.json       # 必须：官方价目数据
                        # config.json / fx-cache.json 首次运行自动生成
```

```bash
sudo mkdir -p /opt/aipc/data
scp aipc-server root@<vps>:/opt/aipc/
scp data/models.json root@<vps>:/opt/aipc/data/
sudo chmod +x /opt/aipc/aipc-server
```

冒烟：`/opt/aipc/aipc-server --port 8787`，另开终端 `curl 127.0.0.1:8787/api/models` 出 JSON 即正常。

## 3. systemd

`/etc/systemd/system/aipc.service`：

```ini
[Unit]
Description=AI price calculator (Go backend)
After=network.target

[Service]
Type=simple
User=aipc                        # 建议建非 root 用户
WorkingDirectory=/opt/aipc
ExecStart=/opt/aipc/aipc-server --host 127.0.0.1 --port 8787
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload && sudo systemctl enable --now aipc
journalctl -u aipc -f            # 看启动日志确认数据目录/前端来源
```

## 4. nginx

```nginx
server {
    listen 80;
    server_name price.example.com;

    location / {
        proxy_pass http://127.0.0.1:8787;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_read_timeout 60s;     # /api/pricing/update 是后台 job，返回快；
                                    # /api/jobs/<id> 轮询，普通超时就够
    }
}
```

HTTPS 照常 certbot。`--host 127.0.0.1` 保证只有本机 nginx 能连——**不要**为了图省事绑定 `0.0.0.0`，管理面（改配置、触发抓取）无鉴权。

## 5. 数据与备份

- 唯一有状态目录：`/opt/aipc/data/`。备份 = 打包它。
- `models.json` 每次价目更新先写 `models.json.bak` 再覆盖（备份失败则中止更新）。
- `config.json`（含站点/档案配置 + `proxySubscription`）由 PUT /api/config 原子写。
- 更新 models.json 的另一条路径：在本地改好后 `scp` 上去——文件即真相，无数据库。

## 6. 故障排查

| 现象 | 查 |
|---|---|
| 页面 404/白屏 | 二进制是否 syncassets 后构建；`--public` 是否误指向不存在目录 |
| 价目更新全 unreachable | VPS 在墙内且未填订阅 → 「官方价目」页填订阅；订阅地址本身需直连可达 |
| job 报「订阅拉取失败」 | `curl <订阅地址>` 在 VPS 上测连通性；看 job 日志的解码方式行 |
| 某源一直直连失败 | 该源墙外不可达属预期——确认订阅已配置，看日志是否进了节点池探测 |
| 启动即退 | `journalctl -u aipc`；常见：`data/` 缺 models.json、端口被占 |

## 7. 升级

```bash
scp aipc-server root@<vps>:/opt/aipc/aipc-server.new
ssh root@<vps> 'systemctl stop aipc && mv /opt/aipc/aipc-server.new /opt/aipc/aipc-server && chmod +x /opt/aipc/aipc-server && systemctl start aipc'
```

回滚：保留旧二进制重命名即可（数据格式向后兼容，config/models 无破坏性变更）。
