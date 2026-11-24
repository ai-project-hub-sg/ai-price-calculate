// aipc-server：AI 价格归一化计算器 Go 版后端（单二进制）。
// 用法：
//
//	aipc-server [--host 127.0.0.1] [--port 8787] [--data ./data] [--public DIR]
//
// --public 指定磁盘目录（开发用，实时生效）；缺省=二进制内嵌的前端文件（embed）。
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/ai-project-hub-sg/ai-price-calculate/server-go/assets"
	"github.com/ai-project-hub-sg/ai-price-calculate/server-go/internal/httpsrv"
)

func main() {
	exeDir, _ := filepath.Abs(filepath.Dir(os.Args[0]))
	host := flag.String("host", "127.0.0.1", "监听地址")
	port := flag.Int("port", 8787, "监听端口")
	data := flag.String("data", filepath.Join(exeDir, "data"),
		"数据目录（models.json/config.json/fx-cache.json），默认=二进制同目录 data/")
	pub := flag.String("public", "", "前端目录（开发用）；空=内嵌 embed")
	flag.Parse()

	publicDir := *pub
	mode := "内嵌 embed"
	if publicDir != "" {
		mode = "磁盘 " + publicDir
	}
	if abs, err := filepath.Abs(*data); err == nil {
		*data = abs
	}
	srv := httpsrv.New(*data, publicDir, assets.Public)
	addr := fmt.Sprintf("%s:%d", *host, *port)
	fmt.Printf("AI 价格归一化计算器 → http://%s\n", addr)
	fmt.Printf("数据目录 %s · 前端来源 %s\n", *data, mode)
	fmt.Println("接口: /api/models /api/config /api/fx /api/nodes " +
		"/api/pricing/update /api/jobs/<id>")
	if err := http.ListenAndServe(addr, srv.Handler()); err != nil {
		fmt.Fprintln(os.Stderr, "服务退出:", err)
		os.Exit(1)
	}
}
