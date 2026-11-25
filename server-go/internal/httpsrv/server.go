// HTTP 层 —— server/app.py 的 Go 移植。端点保持一致：
//
//	GET  /api/models /api/config /api/fx /api/nodes /api/jobs/<id>
//	PUT  /api/config    POST /api/pricing/update    静态文件
//	POST /api/auth/login /api/auth/logout
//
// 显式原则：数据文件损坏返回 500 + 日志，不静默降级为默认/空数据
// （静默降级会让下一次 PUT 无声覆盖损坏文件）。
package httpsrv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ai-project-hub-sg/ai-price-calculate/server-go/internal/auth"
	"github.com/ai-project-hub-sg/ai-price-calculate/server-go/internal/fetch"
	"github.com/ai-project-hub-sg/ai-price-calculate/server-go/internal/proxypool"
)

const (
	fxURL          = "https://open.er-api.com/v6/latest/USD"
	fxTTLS         = 24 * 3600
	jobKeep        = 200 // job log 保留行数
	maxRequestBody = 2 << 20
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
	Status    string         `json:"status"`
	Report    any            `json:"report"`
	Log       []string       `json:"log"`
	Error     string         `json:"error,omitempty"`
	Progress  fetch.Progress `json:"progress,omitempty"`
	StartedAt int64          `json:"startedAt"` // job 创建 epoch ms，前端算总耗时
}

type Server struct {
	DataDir   string
	PublicDir string // 非空 = 磁盘目录优先（开发）；空 = 用 PublicFS
	PublicFS  fs.FS  // embed 的 public/（内容已含 public/ 前缀）

	authManager *auth.Manager
	nodeCache   *proxypool.Cache // 「测试节点」与「价目更新」共享的可用性缓存

	ShutdownFn func() // POST /api/shutdown 调用，main 注入优雅停机

	configMu     sync.Mutex
	mu           sync.Mutex
	jobs         map[string]*job
	runningJobs  int
	shuttingDown bool
	jobsDone     chan struct{} // 非 nil 表示仍有任务；关闭后置 nil，避免重复 close
}

func New(dataDir, publicDir string, publicFS fs.FS, authManager *auth.Manager) *Server {
	if authManager == nil {
		authManager = auth.NewManager("", "") // 未注入管理器时拒绝登录，fail closed
	}
	return &Server{DataDir: dataDir, PublicDir: publicDir,
		PublicFS: publicFS, authManager: authManager, jobs: map[string]*job{},
		nodeCache: proxypool.NewCache(time.Hour)}
}

func (s *Server) modelsPath() string { return filepath.Join(s.DataDir, "models.json") }
func (s *Server) configPath() string { return filepath.Join(s.DataDir, "config.json") }
func (s *Server) fxPath() string     { return filepath.Join(s.DataDir, "fx-cache.json") }

// subURL 从 config.json 读显式代理订阅配置（顶层 proxySubscription 字段）。
// 文件不存在 → ("", nil)；损坏或订阅非法 → ("", err)，由调用方显式处理。
func (s *Server) subURL() (string, error) {
	cfg, err := s.readJSON(s.configPath())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	v, exists := cfg["proxySubscription"]
	if !exists {
		return "", nil
	}
	raw, ok := v.(string)
	if !ok {
		return "", errors.New("config.json 的 proxySubscription 必须是字符串")
	}
	if raw == "" {
		return "", nil
	}
	norm, err := proxypool.ValidateSubscriptionURL(raw)
	if err != nil {
		return "", fmt.Errorf("config.json 的 proxySubscription 无效: %w", err)
	}
	return norm, nil
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
	mux.HandleFunc("POST /api/shutdown", s.hShutdown)
	mux.HandleFunc("GET /api/jobs/", s.hJob)
	mux.HandleFunc("POST /api/auth/login", s.hLogin)
	mux.HandleFunc("POST /api/auth/logout", s.hLogout)
	mux.HandleFunc("OPTIONS /", s.hOptions)
	mux.HandleFunc("GET /", s.hStatic)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			mux.ServeHTTP(w, r)
			return
		}

		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		}
		// 访问日志（仅 API）：定位"job 消失/打错实例"类问题
		sw := &statusWriter{ResponseWriter: w, code: 200}
		loginPath := r.Method == http.MethodPost && r.URL.Path == "/api/auth/login"
		if r.Method != http.MethodOptions && !loginPath {
			token, ok := bearerToken(r.Header.Get("Authorization"))
			if !ok {
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeJSON(sw, http.StatusUnauthorized, map[string]any{"error": "未授权"})
				fmt.Fprintf(os.Stderr, "%s %s → %d\n", r.Method, r.URL.Path, sw.code)
				return
			}
			if _, ok := s.authManager.Authenticate(token); !ok {
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeJSON(sw, http.StatusUnauthorized, map[string]any{"error": "未授权"})
				fmt.Fprintf(os.Stderr, "%s %s → %d\n", r.Method, r.URL.Path, sw.code)
				return
			}
		}
		mux.ServeHTTP(sw, r)
		fmt.Fprintf(os.Stderr, "%s %s → %d\n", r.Method, r.URL.Path, sw.code)
	})
}

func bearerToken(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

// clientKey 只使用 TCP 对端地址；反向代理头由客户端伪造，不能用于限流身份。
func clientKey(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

func (s *Server) hLogin(w http.ResponseWriter, r *http.Request) {
	type loginRequest struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	var req *loginRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	if err := decodeJSONObject(r.Body, &req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "登录请求体不能超过 2MB"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "登录请求体必须是单个 JSON 对象: " + err.Error()})
		return
	}
	if req == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "登录请求体必须是 JSON 对象"})
		return
	}
	session, err := s.authManager.Login(req.Username, req.Password, clientKey(r.RemoteAddr))
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrInvalidCredentials):
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "用户名或密码错误"})
		case errors.Is(err, auth.ErrRateLimited):
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "登录失败次数过多，请稍后重试"})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "登录失败: " + err.Error()})
		}
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) hLogout(w http.ResponseWriter, r *http.Request) {
	token, _ := bearerToken(r.Header.Get("Authorization")) // 中间件已校验
	s.authManager.Logout(token)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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

// decodeJSONObject 要求单个 JSON 值；调用方负责拒绝解码得到的 nil（JSON null）。
// struct 使用 DisallowUnknownFields，拒绝未知字段和尾随 JSON 值。
func decodeJSONObject(r io.Reader, dst any) error {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("只能包含一个 JSON 值")
		}
		return err
	}
	return nil
}

func decodeOptionalJSONObject(r io.Reader, dst any) error {
	err := decodeJSONObject(r, dst)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
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
	if m == nil {
		return nil, fmt.Errorf("%s 必须是 JSON 对象", filepath.Base(path))
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
	if err := decodeJSONObject(r.Body, &cfg); err != nil {
		writeJSON(w, 400, map[string]any{"error": "config JSON 解析失败: " + err.Error()})
		return
	}
	if cfg == nil {
		writeJSON(w, 400, map[string]any{"error": "config 必须是 JSON 对象"})
		return
	}
	if len(cfg) == 0 {
		writeJSON(w, 400, map[string]any{"error": "config 不能为空 JSON 对象"})
		return
	}
	if raw, exists := cfg["proxySubscription"]; exists {
		sub, ok := raw.(string)
		if !ok {
			writeJSON(w, 400, map[string]any{"error": "proxySubscription 必须是字符串"})
			return
		}
		if sub != "" {
			norm, err := proxypool.ValidateSubscriptionURL(sub)
			if err != nil {
				writeJSON(w, 400, map[string]any{"error": "proxySubscription 无效: " + err.Error()})
				return
			}
			cfg["proxySubscription"] = norm
		}
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}

	// 所有写者共用同目录唯一临时文件，串行化 rename，避免并发覆盖和 .tmp 争用。
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if err := os.MkdirAll(s.DataDir, 0o755); err != nil {
		writeJSON(w, 500, map[string]any{"error": "创建数据目录失败: " + err.Error()})
		return
	}
	f, err := os.CreateTemp(s.DataDir, ".config-*.tmp")
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": "创建配置临时文件失败: " + err.Error()})
		return
	}
	tmp := f.Name()
	defer os.Remove(tmp) // 成功 rename 后是 no-op；任何失败都清理临时文件
	defer f.Close()

	// 原子写：写全量 → fsync → close → rename；任一步失败都不碰原文件
	n, werr := f.Write(b)
	if werr == nil && n != len(b) {
		werr = io.ErrShortWrite
	}
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		writeJSON(w, 500, map[string]any{"error": "config 写入失败: " + werr.Error()})
		return
	}
	if err := os.Rename(tmp, s.configPath()); err != nil {
		writeJSON(w, 500, map[string]any{"error": "config 替换失败: " + err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func validFXData(data any) bool {
	m, ok := data.(map[string]any)
	if !ok {
		return false
	}
	rate, ok := m["rate"].(float64)
	return ok && !math.IsNaN(rate) && !math.IsInf(rate, 0) && rate > 0
}

func (s *Server) hFx(w http.ResponseWriter, r *http.Request) {
	refresh := r.URL.Query().Get("refresh") == "1"
	// 缓存可再生，容忍损坏：解析失败直接当无缓存重新拉取（有意为之）
	if !refresh {
		if b, err := os.ReadFile(s.fxPath()); err == nil {
			var cache map[string]any
			if json.Unmarshal(b, &cache) == nil {
				if ts, ok := cache["ts"].(float64); ok && time.Since(time.Unix(int64(ts), 0)).Seconds() < fxTTLS {
					if data, ok := cache["data"].(map[string]any); ok && validFXData(data) {
						data["cached"] = true
						writeJSON(w, 200, data)
						return
					}
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
	if !validFXData(map[string]any{"rate": rates["CNY"]}) {
		writeJSON(w, 502, map[string]any{"error": "汇率源返回的 CNY 汇率无效"})
		return
	}
	fetchedAt, _ := payload["time_last_update_utc"].(string)
	data := map[string]any{
		"rate":      rates["CNY"],
		"source":    fxURL,
		"fetchedAt": fetchedAt,
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
		writeJSON(w, 500, map[string]any{"error": "config 配置无效: " + err.Error()})
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

// startJob 在发布 goroutine 前登记任务，StartedAt 对两类后台任务统一生效。
func (s *Server) startJob(j *job, run func(id string)) string {
	id := randID()
	if j.Status == "" {
		j.Status = "running"
	}
	if j.StartedAt == 0 {
		j.StartedAt = time.Now().UnixMilli()
	}
	s.mu.Lock()
	if s.shuttingDown {
		s.mu.Unlock()
		return ""
	}
	if s.runningJobs == 0 {
		s.jobsDone = make(chan struct{})
	}
	s.runningJobs++
	s.jobs[id] = j
	s.mu.Unlock()
	go s.runJob(id, run)
	return id
}

func (s *Server) runJob(id string, run func(id string)) {
	defer func() {
		if recovered := recover(); recovered != nil {
			s.mu.Lock()
			if j := s.jobs[id]; j != nil {
				j.Status = "error"
				j.Error = fmt.Sprintf("后台任务 panic: %v", recovered)
			}
			s.mu.Unlock()
		}
		s.mu.Lock()
		s.runningJobs--
		if s.runningJobs == 0 {
			close(s.jobsDone)
			s.jobsDone = nil
		}
		s.mu.Unlock()
	}()
	run(id)
}

func (s *Server) StopAcceptingJobs() {
	s.mu.Lock()
	s.shuttingDown = true
	s.mu.Unlock()
}

// WaitForJobs 等待所有已登记后台任务结束；ctx 取消只停止等待，不取消任务。
func (s *Server) WaitForJobs(ctx context.Context) error {
	s.mu.Lock()
	done := s.jobsDone
	s.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// parseMaxNodes maxNodes = 最多探测节点数，纯上限无魔法值：缺省 999
// （实际等同全部）；接受正整数（JSON 数字到 Go 皆是 float64，整数值
// 25.0 与 25 同义）；0/负数/非整数值/非数字报 400。注意是「整数值」
// 而非「允许浮点」——没有 25.1 个节点。
func parseMaxNodes(body map[string]any) (int, error) {
	const hint = "maxNodes 必须是正整数值（≥1，如 25；缺省 999 等同全部节点）"
	raw, exists := body["maxNodes"]
	if !exists || raw == nil {
		return 999, nil
	}
	v, ok := raw.(float64)
	if !ok {
		return 0, fmt.Errorf("%s；收到 %T 类型，不是数字", hint, raw)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%s；收到非有限数字", hint)
	}
	if math.Trunc(v) != v {
		return 0, fmt.Errorf("%s；收到 %v，节点个数不能是小数", hint, v)
	}
	if v < 1 {
		return 0, fmt.Errorf("%s；收到 %v，0 或负数不表示全部——要测全部请缺省或填大数", hint, v)
	}
	if v >= float64(int(^uint(0)>>1)) {
		return 0, fmt.Errorf("%s；收到 %v，超出整数范围", hint, v)
	}
	return int(v), nil
}

func parseProviders(body map[string]any) ([]string, error) {
	raw, exists := body["providers"]
	if !exists || raw == nil {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, errors.New("providers 必须是字符串数组")
	}
	providers := make([]string, 0, len(values))
	for _, value := range values {
		provider, ok := value.(string)
		if !ok {
			return nil, errors.New("providers 必须是字符串数组")
		}
		providers = append(providers, provider)
	}
	return providers, nil
}

// hNodesTest 启动节点测试 job：body {"url"?}（空=用 config.proxySubscription）。
// 拉订阅→逐节点连通性探测→report {total, probed, usable, results[]}。
// 走 job 模型与价目更新同一套轮询机制（探测几十节点要分钟级）。
func (s *Server) hNodesTest(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{}
	if err := decodeOptionalJSONObject(r.Body, &body); err != nil {
		writeJSON(w, 400, map[string]any{"error": "请求体 JSON 解析失败: " + err.Error()})
		return
	}
	if body == nil {
		writeJSON(w, 400, map[string]any{"error": "请求体必须是 JSON 对象"})
		return
	}
	sub := ""
	if raw, exists := body["url"]; exists {
		value, ok := raw.(string)
		if !ok {
			writeJSON(w, 400, map[string]any{"error": "body.url 必须是字符串"})
			return
		}
		sub = value
		if sub != "" {
			norm, err := proxypool.ValidateSubscriptionURL(sub)
			if err != nil {
				writeJSON(w, 400, map[string]any{"error": "body.url 无效: " + err.Error()})
				return
			}
			sub = norm
		}
	}
	if sub == "" {
		var err error
		if sub, err = s.subURL(); err != nil {
			writeJSON(w, 500, map[string]any{"error": "config 配置无效: " + err.Error()})
			return
		}
	}
	if sub == "" {
		writeJSON(w, 400, map[string]any{"error": "未提供订阅地址（body.url 或 config.proxySubscription 均为空）"})
		return
	}
	maxNodes, err := parseMaxNodes(body)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	force, _ := body["force"].(bool)
	id := s.startJob(&job{Status: "running"}, func(id string) {
		s.runNodesTest(id, sub, maxNodes, force)
	})
	if id == "" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "服务正在关闭，无法启动新任务"})
		return
	}
	writeJSON(w, 200, map[string]any{"jobId": id})
}

func (s *Server) runNodesTest(id, sub string, maxNodes int, force bool) {
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
	// 1h 内测过 → 直接回缓存结果（report.cached 标记），不重复全池探测；
	// 要跑新的用「强制重测」（force=true）。
	if !force {
		if res, at, ok := s.nodeCache.CachedTest(sub); ok {
			usable := 0
			for _, r := range res {
				if r.OK {
					usable++
				}
			}
			total := len(s.nodeCache.StaleSubNodes(sub))
			if total == 0 {
				total = len(res)
			}
			logf(fmt.Sprintf("缓存命中：%s 前的探测结果（1h 内有效），重跑请点「强制重测」",
				time.Since(at).Round(time.Second)))
			finish("done", map[string]any{
				"total": total, "probed": len(res), "usable": usable,
				"results": res, "cached": true,
			}, nil)
			return
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
	if probed == 0 || len(nodes) < probed {
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
	s.nodeCache.MarkTestDone(sub, results) // 结果快照入缓存：1h 内再点测试秒回
	logf(fmt.Sprintf("完成：%d/%d 节点可用", usable, len(results)))
	finish("done", map[string]any{
		"total": len(nodes), "probed": len(results),
		"usable": usable, "results": results,
	}, nil)
}

func (s *Server) hUpdate(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{}
	if err := decodeOptionalJSONObject(r.Body, &body); err != nil {
		writeJSON(w, 400, map[string]any{"error": "请求体 JSON 解析失败: " + err.Error()})
		return
	}
	if body == nil {
		writeJSON(w, 400, map[string]any{"error": "请求体必须是 JSON 对象"})
		return
	}
	providers, err := parseProviders(body)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	maxNodes, err := parseMaxNodes(body)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	sub, err := s.subURL()
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": "config 配置无效: " + err.Error()})
		return
	}
	id := s.startJob(&job{Status: "running"}, func(id string) {
		s.runUpdate(id, providers, maxNodes, sub)
	})
	if id == "" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "服务正在关闭，无法启动新任务"})
		return
	}
	writeJSON(w, 200, map[string]any{"jobId": id})
}

func (s *Server) runUpdate(id string, providers []string, maxNodes int, sub string) {
	logf := func(m string) {
		s.mu.Lock()
		j := s.jobs[id]
		j.Log = append(j.Log, m)
		if len(j.Log) > jobKeep {
			j.Log = j.Log[len(j.Log)-jobKeep:]
		}
		s.mu.Unlock()
	}
	rep, err := fetch.Update(fetch.Options{
		Providers: providers, MaxNodes: maxNodes,
		SubURL: sub, ModelsPath: s.modelsPath(), Log: logf,
		NodeCache: s.nodeCache,
		Progress: func(p fetch.Progress) {
			s.mu.Lock()
			s.jobs[id].Progress = p
			s.mu.Unlock()
		}})
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
	if !ok || j == nil {
		s.mu.Unlock()
		writeJSON(w, 404, map[string]any{"error": "not found"})
		return
	}
	// 序列化可能较慢；只在锁内复制可变字段，解锁后写响应。
	snapshot := *j
	snapshot.Log = append([]string(nil), j.Log...)
	snapshot.Progress.Current = append([]string(nil), j.Progress.Current...)
	snapshot.Progress.Targets = append([]string(nil), j.Progress.Targets...)
	snapshot.Progress.Done = append([]string(nil), j.Progress.Done...)
	s.mu.Unlock()
	writeJSON(w, 200, snapshot)
}

// hShutdown 关闭后端（前端「关闭服务」按钮）。先回响应再延时停机，
// 保证前端能拿到确认；运行中的任务必须先结束，避免优雅停机丢任务。
func (s *Server) hShutdown(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.runningJobs > 0 || s.shuttingDown {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{"error": "仍有后台任务运行中或服务正在关闭"})
		return
	}
	s.shuttingDown = true
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"ok": true, "shutdown": true})
	if s.ShutdownFn != nil {
		go func() {
			time.Sleep(300 * time.Millisecond)
			s.ShutdownFn()
		}()
	}
}

func (s *Server) hOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Methods", "GET,PUT,POST,OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
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
