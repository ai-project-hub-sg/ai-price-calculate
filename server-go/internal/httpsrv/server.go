// HTTP 层 —— server/app.py 的 Go 移植。端点保持一致：
//   GET  /api/models /api/config /api/fx /api/nodes /api/jobs/<id>
//   PUT  /api/config    POST /api/pricing/update    静态文件
// 显式原则：数据文件损坏返回 500 + 日志，不静默降级为默认/空数据
// （静默降级会让下一次 PUT 无声覆盖损坏文件）。
package httpsrv

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ai-project-hub-sg/ai-price-calculate/server-go/internal/fetch"
	"github.com/ai-project-hub-sg/ai-price-calculate/server-go/internal/proxypool"
)

const (
	fxURL   = "https://open.er-api.com/v6/latest/USD"
	fxTTLS  = 24 * 3600
	jobKeep = 200 // job log 保留行数
)

var defaultConfig = map[string]any{
	"version": 1,
	"scenario": map[string]any{"inputTokens": 20000, "outputTokens": 4000,
		"cacheReadRatio": 0.3, "requests": 1},
	"tieredPricing": map[string]any{"enabled": true},
	"currency":      map[string]any{"display": "both", "usdCny": nil, "autoFx": true},
	"defaults":      map[string]any{"discount": 1.0},
	"stations":      []any{},
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

type job struct {
	Status string   `json:"status"`
	Report any      `json:"report"`
	Log    []string `json:"log"`
	Error  string   `json:"error,omitempty"`
}

type Server struct {
	DataDir   string
	PublicDir string // 非空 = 磁盘目录优先（开发）；空 = 用 PublicFS
	PublicFS  fs.FS  // embed 的 public/（内容已含 public/ 前缀）

	nodeCache *proxypool.Cache // 「测试节点」与「价目更新」共享的可用性缓存

	mu   sync.Mutex
	jobs map[string]*job
}

func New(dataDir, publicDir string, publicFS fs.FS) *Server {
	return &Server{DataDir: dataDir, PublicDir: publicDir,
		PublicFS: publicFS, jobs: map[string]*job{},
		nodeCache: proxypool.NewCache(time.Hour)}
}

func (s *Server) modelsPath() string { return filepath.Join(s.DataDir, "models.json") }
func (s *Server) configPath() string { return filepath.Join(s.DataDir, "config.json") }
func (s *Server) fxPath() string     { return filepath.Join(s.DataDir, "fx-cache.json") }

// subURL 从 config.json 读显式代理订阅配置（顶层 proxySubscription 字段）。
// 文件不存在 → ("", nil)；损坏 → ("", err) 由调用方显式处理。
func (s *Server) subURL() (string, error) {
	cfg, err := s.readJSON(s.configPath())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	v, _ := cfg["proxySubscription"].(string)
	return strings.TrimSpace(v), nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/models", s.hModels)
	mux.HandleFunc("GET /api/config", s.hGetConfig)
	mux.HandleFunc("PUT /api/config", s.hPutConfig)
	mux.HandleFunc("GET /api/fx", s.hFx)
	mux.HandleFunc("GET /api/nodes", s.hNodes)
	mux.HandleFunc("POST /api/nodes/test", s.hNodesTest)
	mux.HandleFunc("POST /api/pricing/update", s.hUpdate)
	mux.HandleFunc("GET /api/jobs/", s.hJob)
	mux.HandleFunc("OPTIONS /", s.hOptions)
	mux.HandleFunc("GET /", s.hStatic)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			// 访问日志（仅 API）：定位"job 消失/打错实例"类问题
			sw := &statusWriter{ResponseWriter: w, code: 200}
			mux.ServeHTTP(sw, r)
			fmt.Fprintf(os.Stderr, "%s %s → %d\n", r.Method, r.URL.Path, sw.code)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		code, b = 500, []byte(`{"error":"marshal"}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	w.Write(b)
}

// readJSON 读 JSON 文件：不存在返回 defaultVal；损坏返回错误（不静默降级）。
func (s *Server) readJSON(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s 解析失败: %w", filepath.Base(path), err)
	}
	return m, nil
}

func (s *Server) hModels(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile(s.modelsPath())
	if os.IsNotExist(err) {
		writeJSON(w, 200, map[string]any{})
		return
	}
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil { // 显式报错，不下发坏数据
		writeJSON(w, 500, map[string]any{"error": "models.json 解析失败: " + err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(b)
}

func (s *Server) hGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.readJSON(s.configPath())
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, 200, defaultConfig)
			return
		}
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, cfg)
}

func (s *Server) hPutConfig(w http.ResponseWriter, r *http.Request) {
	var cfg map[string]any
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSON(w, 400, map[string]any{"error": "config JSON 解析失败: " + err.Error()})
		return
	}
	os.MkdirAll(s.DataDir, 0o755)
	tmp := s.configPath() + ".tmp"
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	// 原子写：写全量 → fsync → rename；任一步失败都不碰原文件
	werr := error(nil)
	if _, werr = f.Write(b); werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(tmp)
		writeJSON(w, 500, map[string]any{"error": "config 写入失败: " + werr.Error()})
		return
	}
	if err := os.Rename(tmp, s.configPath()); err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) hFx(w http.ResponseWriter, r *http.Request) {
	// 缓存可再生，容忍损坏：解析失败直接当无缓存重新拉取（有意为之）
	if b, err := os.ReadFile(s.fxPath()); err == nil {
		var cache map[string]any
		if json.Unmarshal(b, &cache) == nil {
			if ts, ok := cache["ts"].(float64); ok && time.Since(time.Unix(int64(ts), 0)).Seconds() < fxTTLS {
				if data, ok := cache["data"].(map[string]any); ok {
					data["cached"] = true
					writeJSON(w, 200, data)
					return
				}
			}
		}
	}
	res := proxypool.FetchURL(fxURL, 0, 10_000)
	if res.Error != "" || res.Status != 200 {
		writeJSON(w, 502, map[string]any{"error": "汇率获取失败 " + res.Error})
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(res.Body, &payload); err != nil {
		writeJSON(w, 502, map[string]any{"error": "汇率响应解析失败"})
		return
	}
	rates, _ := payload["rates"].(map[string]any)
	data := map[string]any{
		"rate":      rates["CNY"],
		"source":    fxURL,
		"fetchedAt": payload["time_last_update_utc"],
		"cached":    false,
	}
	os.MkdirAll(s.DataDir, 0o755)
	if b, err := json.Marshal(map[string]any{"ts": float64(time.Now().Unix()), "data": map[string]any{
		"rate": data["rate"], "source": data["source"], "fetchedAt": data["fetchedAt"]}}); err == nil {
		os.WriteFile(s.fxPath(), b, 0o644)
	}
	writeJSON(w, 200, data)
}

func (s *Server) hNodes(w http.ResponseWriter, r *http.Request) {
	sub, err := s.subURL()
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": "config 读取失败: " + err.Error()})
		return
	}
	if sub == "" {
		writeJSON(w, 200, map[string]any{"total": 0, "nodes": []any{},
			"hint": "未配置代理订阅地址（config.proxySubscription）"})
		return
	}
	nodes, err := proxypool.LoadSubscription(sub, nil)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	list := make([]any, 0, len(nodes))
	for _, n := range nodes {
		list = append(list, map[string]any{"id": n.ID, "name": n.Name, "kind": n.Kind, "delay": -1})
	}
	writeJSON(w, 200, map[string]any{"total": len(list), "nodes": list})
}

// probeURL 「测试节点」的连通性探测目标：判节点能否出外网，非价目抓取。
const probeURL = "https://www.gstatic.com/generate_204"

// hNodesTest 启动节点测试 job：body {"url"?}（空=用 config.proxySubscription）。
// 拉订阅→逐节点连通性探测→report {total, probed, usable, results[]}。
// 走 job 模型与价目更新同一套轮询机制（探测几十节点要分钟级）。
func (s *Server) hNodesTest(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && r.ContentLength != 0 {
		writeJSON(w, 400, map[string]any{"error": "请求体 JSON 解析失败: " + err.Error()})
		return
	}
	sub, _ := body["url"].(string)
	sub = strings.TrimSpace(sub)
	if sub == "" {
		var err error
		if sub, err = s.subURL(); err != nil {
			writeJSON(w, 500, map[string]any{"error": "config 读取失败: " + err.Error()})
			return
		}
	}
	if sub == "" {
		writeJSON(w, 400, map[string]any{"error": "未提供订阅地址（body.url 或 config.proxySubscription 均为空）"})
		return
	}
	maxNodes := 25
	if v, ok := body["maxNodes"].(float64); ok && v > 0 {
		maxNodes = int(v)
	}
	id := randID()
	j := &job{Status: "running"}
	s.mu.Lock()
	s.jobs[id] = j
	s.mu.Unlock()
	go s.runNodesTest(id, sub, maxNodes)
	writeJSON(w, 200, map[string]any{"jobId": id})
}

func (s *Server) runNodesTest(id, sub string, maxNodes int) {
	logf := func(m string) {
		s.mu.Lock()
		j := s.jobs[id]
		j.Log = append(j.Log, m)
		if len(j.Log) > jobKeep {
			j.Log = j.Log[len(j.Log)-jobKeep:]
		}
		s.mu.Unlock()
	}
	finish := func(status string, rep any, err error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		j := s.jobs[id]
		j.Status = status
		if err != nil {
			j.Error = err.Error()
		} else {
			j.Report = rep
		}
	}
	nodes, err := proxypool.LoadSubscription(sub, logf)
	if err != nil {
		if stale := s.nodeCache.StaleSubNodes(sub); len(stale) > 0 {
			logf(fmt.Sprintf("订阅拉取失败（%s），回退缓存节点列表 %d 个（可能过期）",
				err.Error(), len(stale)))
			nodes = stale
		} else {
			finish("error", nil, err)
			return
		}
	} else {
		s.nodeCache.MarkSub(sub, nodes) // 列表入缓存：更新时免重拉
	}
	probed := maxNodes
	if len(nodes) < probed {
		probed = len(nodes)
	}
	logf(fmt.Sprintf("探测前 %d/%d 节点（连通性目标 %s）…", probed, len(nodes), probeURL))
	results := proxypool.TestNodes(nodes, probeURL, maxNodes, 4, 10_000,
		s.nodeCache, sub,
		func(tested, total int) {
			logf(fmt.Sprintf("  已测 %d/%d", tested, total))
		})
	usable := 0
	for _, r := range results {
		if r.OK {
			usable++
		}
	}
	logf(fmt.Sprintf("完成：%d/%d 节点可用", usable, len(results)))
	finish("done", map[string]any{
		"total": len(nodes), "probed": len(results),
		"usable": usable, "results": results,
	}, nil)
}

func (s *Server) hUpdate(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && r.ContentLength != 0 {
		writeJSON(w, 400, map[string]any{"error": "请求体 JSON 解析失败: " + err.Error()})
		return
	}
	var providers []string
	if v, ok := body["providers"].([]any); ok {
		for _, p := range v {
			if ps, ok := p.(string); ok {
				providers = append(providers, ps)
			}
		}
	}
	maxNodes := 25
	if v, ok := body["maxNodes"].(float64); ok && v > 0 {
		maxNodes = int(v)
	}
	sub, subErr := s.subURL()
	id := randID()
	j := &job{Status: "running"}
	s.mu.Lock()
	s.jobs[id] = j
	s.mu.Unlock()
	go s.runUpdate(id, providers, maxNodes, sub, subErr)
	writeJSON(w, 200, map[string]any{"jobId": id})
}

func (s *Server) runUpdate(id string, providers []string, maxNodes int, sub string, subErr error) {
	logf := func(m string) {
		s.mu.Lock()
		j := s.jobs[id]
		j.Log = append(j.Log, m)
		if len(j.Log) > jobKeep {
			j.Log = j.Log[len(j.Log)-jobKeep:]
		}
		s.mu.Unlock()
	}
	if subErr != nil { // config 损坏：显式声明按未配置处理，不静默
		logf("config.json 读取失败（" + subErr.Error() + "），按未配置代理订阅处理")
	}
	rep, err := fetch.Update(fetch.Options{
		Providers: providers, MaxNodes: maxNodes,
		SubURL: sub, ModelsPath: s.modelsPath(), Log: logf,
		NodeCache: s.nodeCache})
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[id]
	if err != nil {
		j.Status, j.Error = "error", err.Error()
	} else {
		j.Status, j.Report = "done", rep
	}
}

func (s *Server) hJob(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/jobs/")
	s.mu.Lock()
	j, ok := s.jobs[id]
	s.mu.Unlock()
	if !ok {
		writeJSON(w, 404, map[string]any{"error": "not found"})
		return
	}
	writeJSON(w, 200, j)
}

func (s *Server) hOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Methods", "GET,PUT,POST,OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.WriteHeader(204)
}

var mimeTypes = map[string]string{
	".html": "text/html; charset=utf-8", ".js": "text/javascript",
	".css": "text/css", ".json": "application/json; charset=utf-8",
	".svg": "image/svg+xml", ".ico": "image/x-icon",
}

func (s *Server) hStatic(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/")
	if rel == "" {
		rel = "index.html"
	}
	clean := filepath.Clean(rel)
	if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		http.Error(w, "not found", 404)
		return
	}
	if s.PublicDir != "" {
		fp := filepath.Join(s.PublicDir, clean)
		if st, err := os.Stat(fp); err == nil && !st.IsDir() {
			http.ServeFile(w, r, fp)
			return
		}
	}
	if s.PublicFS != nil {
		if b, err := fs.ReadFile(s.PublicFS, filepath.ToSlash(filepath.Join("public", clean))); err == nil {
			w.Header().Set("Content-Type", mimeTypes[filepath.Ext(clean)])
			if w.Header().Get("Content-Type") == "" {
				w.Header().Set("Content-Type", "application/octet-stream")
			}
			w.Write(b)
			return
		}
	}
	http.Error(w, "not found", 404)
}

func randID() string {
	var b [4]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
