// aipc-server：AI 价格归一化计算器 Go 版后端（单二进制）。
// 用法：
//
//	aipc-server [--host 127.0.0.1] [--port 8787] [--data ./data] [--public DIR]
//
// --public 指定磁盘目录（开发用，实时生效）；缺省=二进制内嵌的前端文件（embed）。
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/ai-project-hub-sg/ai-price-calculate/server-go/assets"
	"github.com/ai-project-hub-sg/ai-price-calculate/server-go/internal/auth"
	"github.com/ai-project-hub-sg/ai-price-calculate/server-go/internal/httpsrv"
)

func defaultDataDir() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("定位可执行文件失败: %w", err)
	}
	return filepath.Dir(executable), nil
}

func main() {
	exeDir, err := defaultDataDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
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
	authManager, err := auth.LoadManager()
	if err != nil {
		fmt.Fprintln(os.Stderr, "登录凭据加载失败:", err)
		os.Exit(1)
	}
	srv := httpsrv.New(*data, publicDir, assets.Public, authManager)
	addr := fmt.Sprintf("%s:%d", *host, *port)
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	var shutdownOnce sync.Once
	shutdown := func() {
		shutdownOnce.Do(func() {
			waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			fmt.Fprintln(os.Stderr, "等待后台任务完成…")
			srv.StopAcceptingJobs()
			if err := srv.WaitForJobs(waitCtx); err != nil {
				fmt.Fprintln(os.Stderr, "后台任务等待超时，安全退出:", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			httpSrv.Shutdown(ctx)
		})
	}
	srv.ShutdownFn = shutdown
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-sigCtx.Done()
		shutdown()
	}()
	fmt.Printf("AI 价格归一化计算器 → http://%s\n", addr)
	fmt.Printf("数据目录 %s · 前端来源 %s\n", *data, mode)
	fmt.Println("接口: /api/models /api/config /api/fx /api/nodes " +
		"/api/pricing/update /api/jobs/<id> /api/shutdown")
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "服务退出:", err)
		os.Exit(1)
	}
}
