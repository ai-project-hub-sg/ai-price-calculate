package httpsrv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ai-project-hub-sg/ai-price-calculate/server-go/internal/auth"
	"github.com/ai-project-hub-sg/ai-price-calculate/server-go/internal/fetch"
	"github.com/ai-project-hub-sg/ai-price-calculate/server-go/internal/proxypool"
)

const (
	testUsername = "admin"
	testPassword = "offline-test-password"
	testRemote   = "192.0.2.10:43210"
)

func newTestServer(t *testing.T) (*Server, *auth.Manager, http.Handler) {
	t.Helper()
	manager := auth.NewManager(testUsername, testPassword)
	publicFS := fstest.MapFS{
		"public/index.html": &fstest.MapFile{Data: []byte("static-ok")},
	}
	server := New(t.TempDir(), "", publicFS, manager)
	return server, manager, server.Handler()
}

func performRequest(handler http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = testRemote
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func loginToken(t *testing.T, handler http.Handler) string {
	t.Helper()
	body := fmt.Sprintf(`{"username":%q,"password":%q}`, testUsername, testPassword)
	rec := performRequest(handler, http.MethodPost, "/api/auth/login", body, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var session auth.Session
	if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if session.Token == "" || session.Username != testUsername || session.ExpiresAt == 0 {
		t.Fatalf("invalid session: %+v", session)
	}
	return session.Token
}

func requireErrorJSON(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response is not JSON: %v; body = %s", err, rec.Body.String())
	}
	if message, ok := payload["error"].(string); !ok || message == "" {
		t.Fatalf("response lacks error string: %s", rec.Body.String())
	}
}

func jobCount(server *Server) int {
	server.mu.Lock()
	defer server.mu.Unlock()
	return len(server.jobs)
}

func waitForJobs(t *testing.T, server *Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.WaitForJobs(ctx); err != nil {
		t.Fatalf("WaitForJobs: %v", err)
	}
}

func TestAuthenticationAndCORS(t *testing.T) {
	_, _, handler := newTestServer(t)

	protected := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/models", ""},
		{http.MethodGet, "/api/config", ""},
		{http.MethodPut, "/api/config", `{}`},
		{http.MethodGet, "/api/fx", ""},
		{http.MethodGet, "/api/nodes", ""},
		{http.MethodPost, "/api/nodes/test", `{}`},
		{http.MethodPost, "/api/pricing/update", `{}`},
		{http.MethodPost, "/api/shutdown", ""},
		{http.MethodGet, "/api/jobs/missing", ""},
		{http.MethodPost, "/api/auth/logout", ""},
		{http.MethodGet, "/api/auth/login", ""},
	}
	for _, tc := range protected {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := performRequest(handler, tc.method, tc.path, tc.body, "")
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body = %s", rec.Code, rec.Body.String())
			}
			requireErrorJSON(t, rec)
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
				t.Fatalf("CORS origin = %q, want *", got)
			}
		})
	}

	for _, authorization := range []string{"Basic abc", "Bearer", "Bearer one two"} {
		req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
		req.Header.Set("Authorization", authorization)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("Authorization %q status = %d, want 401", authorization, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodOptions, "/api/config", nil)
	req.Header.Set("Origin", "https://untrusted.example")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("OPTIONS CORS origin = %q, want *", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "Authorization") {
		t.Fatalf("Allow-Headers = %q, want Authorization", got)
	}

	static := performRequest(handler, http.MethodGet, "/", "", "")
	if static.Code != http.StatusOK || static.Body.String() != "static-ok" {
		t.Fatalf("static response = %d %q", static.Code, static.Body.String())
	}

	badLogin := performRequest(handler, http.MethodPost, "/api/auth/login",
		fmt.Sprintf(`{"username":%q,"password":"wrong"}`, testUsername), "")
	if badLogin.Code != http.StatusUnauthorized {
		t.Fatalf("bad login status = %d, want 401", badLogin.Code)
	}
	requireErrorJSON(t, badLogin)

	token := loginToken(t, handler)
	authorized := performRequest(handler, http.MethodGet, "/api/config", "", token)
	if authorized.Code != http.StatusOK {
		t.Fatalf("Bearer config status = %d, body = %s", authorized.Code, authorized.Body.String())
	}

	logout := performRequest(handler, http.MethodPost, "/api/auth/logout", "", token)
	if logout.Code != http.StatusOK {
		t.Fatalf("logout status = %d, body = %s", logout.Code, logout.Body.String())
	}
	revoked := performRequest(handler, http.MethodGet, "/api/config", "", token)
	if revoked.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token status = %d, want 401", revoked.Code)
	}
}

func TestLoginRejectsInvalidJSONAndOversizeBody(t *testing.T) {
	_, _, handler := newTestServer(t)
	invalid := []string{
		"",
		"{",
		"null",
		"[]",
		`"text"`,
		`{} {}`,
		`{"username":"admin","password":123}`,
		`{"username":"admin","password":"offline-test-password","extra":true}`,
	}
	for _, body := range invalid {
		rec := performRequest(handler, http.MethodPost, "/api/auth/login", body, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q status = %d, want 400; response = %s", body, rec.Code, rec.Body.String())
		}
	}

	oversize := `{"username":"admin","password":"` + strings.Repeat("x", maxRequestBody) + `"}`
	rec := performRequest(handler, http.MethodPost, "/api/auth/login", oversize, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversize login status = %d, want 400", rec.Code)
	}

	// 格式错误不应进入密码失败计数；此处仍能正常登录。
	loginToken(t, handler)
}

func TestLoginRateLimitIgnoresForwardedFor(t *testing.T) {
	_, _, handler := newTestServer(t)
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(
			fmt.Sprintf(`{"username":%q,"password":"wrong"}`, testUsername)))
		req.RemoteAddr = testRemote
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i+1))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d status = %d, want 401", i+1, rec.Code)
		}
	}

	body := fmt.Sprintf(`{"username":%q,"password":%q}`, testUsername, testPassword)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	req.RemoteAddr = testRemote
	req.Header.Set("X-Forwarded-For", "203.0.113.99")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limited status = %d, want 429; body = %s", rec.Code, rec.Body.String())
	}
	requireErrorJSON(t, rec)

	// 不同 RemoteAddr 是不同限流身份。
	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	req.RemoteAddr = "192.0.2.11:43210"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("different client status = %d, want 200", rec.Code)
	}
}

func TestPutConfigValidationDoesNotOverwrite(t *testing.T) {
	server, _, handler := newTestServer(t)
	token := loginToken(t, handler)
	original := []byte(`{"version":1,"marker":"original"}`)
	if err := os.WriteFile(server.configPath(), original, 0o600); err != nil {
		t.Fatal(err)
	}

	invalidBodies := []string{
		`{"version":`,
		`null`,
		`[]`,
		`{}`,
		`{} {}`,
		`{"proxySubscription":"not-a-url"}`,
		`{"proxySubscription":"file:///etc/passwd"}`,
		`{"proxySubscription":"ftp://example.invalid/sub"}`,
		`{"proxySubscription":"   "}`,
		`{"proxySubscription":42}`,
		`{"proxySubscription":null}`,
	}
	for _, body := range invalidBodies {
		rec := performRequest(handler, http.MethodPut, "/api/config", body, token)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q status = %d, want 400; response = %s", body, rec.Code, rec.Body.String())
		}
		got, err := os.ReadFile(server.configPath())
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(original) {
			t.Fatalf("invalid body %q overwrote config: %s", body, got)
		}
	}

	validBodies := []string{
		`{"version":1}`,
		`{"version":1,"proxySubscription":""}`,
		`{"version":1,"proxySubscription":"https://example.invalid/sub"}`,
	}
	for _, body := range validBodies {
		rec := performRequest(handler, http.MethodPut, "/api/config", body, token)
		if rec.Code != http.StatusOK {
			t.Fatalf("valid body %q status = %d; body = %s", body, rec.Code, rec.Body.String())
		}
		var cfg map[string]any
		data, err := os.ReadFile(server.configPath())
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			t.Fatalf("saved config is invalid: %v", err)
		}
	}

	// 首尾空白与 Python 一致：strip 后规范化存储而非拒绝。
	rec := performRequest(handler, http.MethodPut, "/api/config",
		`{"version":1,"proxySubscription":"  https://example.invalid/sub?value=fake  "}`, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("whitespace-padded URL status = %d; body = %s", rec.Code, rec.Body.String())
	}
	data, err := os.ReadFile(server.configPath())
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["proxySubscription"] != "https://example.invalid/sub?value=fake" {
		t.Fatalf("proxySubscription stored = %v, want trimmed", cfg["proxySubscription"])
	}

	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	badServer := New(blocked, "", nil, auth.NewManager(testUsername, testPassword))
	badToken := loginToken(t, badServer.Handler())
	rec = performRequest(badServer.Handler(), http.MethodPut, "/api/config", `{"version":1}`, badToken)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("MkdirAll failure status = %d, want 500", rec.Code)
	}
}

func TestConcurrentConfigPuts(t *testing.T) {
	server, _, handler := newTestServer(t)
	token := loginToken(t, handler)
	const writers = 30
	start := make(chan struct{})
	results := make(chan int, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			body := fmt.Sprintf(`{"version":1,"writer":%d}`, i)
			results <- performRequest(handler, http.MethodPut, "/api/config", body, token).Code
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	for code := range results {
		if code != http.StatusOK {
			t.Fatalf("concurrent PUT status = %d, want 200", code)
		}
	}

	data, err := os.ReadFile(server.configPath())
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("final config is invalid JSON: %v; data = %s", err, data)
	}
	if _, ok := cfg["writer"].(float64); !ok {
		t.Fatalf("final config has no writer marker: %s", data)
	}
	temps, err := filepath.Glob(filepath.Join(server.DataDir, ".config-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temps) != 0 {
		t.Fatalf("temporary files left behind: %v", temps)
	}
}

func TestInvalidSubscriptionsDoNotStartJobs(t *testing.T) {
	server, _, handler := newTestServer(t)
	token := loginToken(t, handler)
	for _, body := range []string{
		`{"url":"file:///etc/passwd"}`,
		`{"url":"ftp://example.invalid/sub"}`,
		`{"url":"   "}`,
		`{"url":123}`,
		`{"url":`,
	} {
		rec := performRequest(handler, http.MethodPost, "/api/nodes/test", body, token)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("nodes/test body %q status = %d, want 400", body, rec.Code)
		}
	}
	if got := jobCount(server); got != 0 {
		t.Fatalf("invalid nodes/test created %d jobs", got)
	}

	if err := os.WriteFile(server.configPath(), []byte(`{"proxySubscription":"file:///etc/passwd"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/pricing/update", `{}`},
		{http.MethodPost, "/api/nodes/test", `{}`},
		{http.MethodGet, "/api/nodes", ""},
	} {
		rec := performRequest(handler, tc.method, tc.path, tc.body, token)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s %s status = %d, want 500; body = %s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
		requireErrorJSON(t, rec)
	}
	if got := jobCount(server); got != 0 {
		t.Fatalf("invalid saved subscription created %d jobs", got)
	}

	if err := os.WriteFile(server.configPath(), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := performRequest(handler, http.MethodPost, "/api/pricing/update", `{}`, token)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("corrupt config update status = %d, want 500", rec.Code)
	}
	if got := jobCount(server); got != 0 {
		t.Fatalf("corrupt config created %d jobs", got)
	}
}

func TestBothJobTypesSetStartedAtOffline(t *testing.T) {
	server, _, handler := newTestServer(t)
	token := loginToken(t, handler)

	// 预热节点测试缓存，runNodesTest 直接完成，不拉订阅、不探测外网。
	sub := "https://example.invalid/sub"
	server.nodeCache.MarkTestDone(sub, []proxypool.NodeTestResult{{ID: "sub-0", Kind: "vless-reality", OK: true}})
	nodesRec := performRequest(handler, http.MethodPost, "/api/nodes/test", fmt.Sprintf(`{"url":%q}`, sub), token)
	if nodesRec.Code != http.StatusOK {
		t.Fatalf("nodes/test status = %d; body = %s", nodesRec.Code, nodesRec.Body.String())
	}
	var nodesStart struct {
		JobID string `json:"jobId"`
	}
	if err := json.Unmarshal(nodesRec.Body.Bytes(), &nodesStart); err != nil {
		t.Fatal(err)
	}
	waitForJobs(t, server)
	nodesJob := performRequest(handler, http.MethodGet, "/api/jobs/"+nodesStart.JobID, "", token)
	var nodesSnapshot job
	if err := json.Unmarshal(nodesJob.Body.Bytes(), &nodesSnapshot); err != nil {
		t.Fatal(err)
	}
	if nodesSnapshot.StartedAt <= 0 || nodesSnapshot.Status != "done" {
		t.Fatalf("nodes job StartedAt/status = %d/%s", nodesSnapshot.StartedAt, nodesSnapshot.Status)
	}

	// 损坏的 models.json 让 fetch.Update 在任何联网抓取前返回错误。
	if err := os.WriteFile(server.modelsPath(), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	updateRec := performRequest(handler, http.MethodPost, "/api/pricing/update", `{}`, token)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("pricing/update status = %d; body = %s", updateRec.Code, updateRec.Body.String())
	}
	var updateStart struct {
		JobID string `json:"jobId"`
	}
	if err := json.Unmarshal(updateRec.Body.Bytes(), &updateStart); err != nil {
		t.Fatal(err)
	}
	waitForJobs(t, server)
	updateJob := performRequest(handler, http.MethodGet, "/api/jobs/"+updateStart.JobID, "", token)
	var updateSnapshot job
	if err := json.Unmarshal(updateJob.Body.Bytes(), &updateSnapshot); err != nil {
		t.Fatal(err)
	}
	if updateSnapshot.StartedAt <= 0 || updateSnapshot.Status != "error" {
		t.Fatalf("update job StartedAt/status = %d/%s", updateSnapshot.StartedAt, updateSnapshot.Status)
	}
}

func TestJobSnapshotConcurrentReadWrite(t *testing.T) {
	server, _, _ := newTestServer(t)
	j := &job{
		Status: "running",
		Log:    []string{"log-0"},
		Progress: fetch.Progress{
			Current: []string{"current-0"},
			Targets: []string{"target-0"},
			Done:    []string{"done-0"},
		},
	}
	release := make(chan struct{})
	id := server.startJob(j, func(string) { <-release })

	stopWriter := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for i := 1; ; i++ {
			select {
			case <-stopWriter:
				return
			default:
			}
			server.mu.Lock()
			j.Log[0] = fmt.Sprintf("log-%d", i)
			j.Progress.Current[0] = fmt.Sprintf("current-%d", i)
			j.Progress.Targets[0] = fmt.Sprintf("target-%d", i)
			j.Progress.Done[0] = fmt.Sprintf("done-%d", i)
			server.mu.Unlock()
			runtime.Gosched()
		}
	}()

	const readers = 4
	const iterations = 200
	start := make(chan struct{})
	errCh := make(chan error, readers)
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for n := 0; n < iterations; n++ {
				req := httptest.NewRequest(http.MethodGet, "/api/jobs/"+id, nil)
				rec := httptest.NewRecorder()
				server.hJob(rec, req)
				if rec.Code != http.StatusOK {
					errCh <- fmt.Errorf("hJob status = %d", rec.Code)
					return
				}
				var snapshot job
				if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
					errCh <- fmt.Errorf("decode snapshot: %w", err)
					return
				}
				if len(snapshot.Log) != 1 || len(snapshot.Progress.Current) != 1 ||
					len(snapshot.Progress.Targets) != 1 || len(snapshot.Progress.Done) != 1 {
					errCh <- fmt.Errorf("snapshot slices were truncated: %+v", snapshot)
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(stopWriter)
	<-writerDone
	close(release)
	waitForJobs(t, server)
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func TestShutdownConflictWaitForJobsAndPanicRecovery(t *testing.T) {
	server, _, handler := newTestServer(t)
	token := loginToken(t, handler)
	var shutdownCalled atomic.Bool
	server.ShutdownFn = func() { shutdownCalled.Store(true) }

	release := make(chan struct{})
	server.startJob(&job{Status: "running"}, func(string) { <-release })
	rec := performRequest(handler, http.MethodPost, "/api/shutdown", "", token)
	if rec.Code != http.StatusConflict {
		t.Fatalf("shutdown with running job status = %d, want 409; body = %s", rec.Code, rec.Body.String())
	}
	requireErrorJSON(t, rec)
	if shutdownCalled.Load() {
		t.Fatal("ShutdownFn called while a job was running")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := server.WaitForJobs(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitForJobs canceled context error = %v, want context.Canceled", err)
	}
	close(release)
	waitForJobs(t, server)

	panicID := server.startJob(&job{Status: "running"}, func(string) { panic("offline boom") })
	waitForJobs(t, server)
	panicRec := performRequest(handler, http.MethodGet, "/api/jobs/"+panicID, "", token)
	var panicked job
	if err := json.Unmarshal(panicRec.Body.Bytes(), &panicked); err != nil {
		t.Fatal(err)
	}
	if panicked.Status != "error" || !strings.Contains(panicked.Error, "offline boom") {
		t.Fatalf("panic was not recorded: %+v", panicked)
	}
}

func TestJobInputValidation(t *testing.T) {
	server, _, handler := newTestServer(t)
	token := loginToken(t, handler)
	cases := []struct {
		path string
		body string
	}{
		{"/api/nodes/test", `{"url":"https://example.com/sub","maxNodes":"25"}`},
		{"/api/nodes/test", `{"url":"https://example.com/sub","maxNodes":-1}`},
		{"/api/nodes/test", `{"url":"https://example.com/sub","maxNodes":0}`},
		{"/api/nodes/test", `{"url":"https://example.com/sub","maxNodes":1.5}`},
		{"/api/nodes/test", `{"url":"https://example.com/sub","maxNodes":25.1}`},
		{"/api/pricing/update", `{"providers":"openai"}`},
		{"/api/pricing/update", `{"providers":["openai",7]}`},
		{"/api/pricing/update", `{"maxNodes":false}`},
	}
	for _, tc := range cases {
		rec := performRequest(handler, http.MethodPost, tc.path, tc.body, token)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s status = %d, want 400; body = %s",
				tc.path, tc.body, rec.Code, rec.Body.String())
		}
		requireErrorJSON(t, rec)
	}
	if got := jobCount(server); got != 0 {
		t.Fatalf("invalid requests started %d jobs", got)
	}
}

// parseMaxNodes 整数值语义：25.0 与 25 同义（JSON 数字到 Go 皆是 float64），
// 缺省/null → 999（实际等同全部）；0/负数/非整数值/非数字拒绝。
// 是「整数值」而非「允许浮点」——没有 25.1 个节点。
// CR-005 上限：>9999 直接拒绝（极限大值只可能是输错），999<x≤9999 接受
// 但由 maxNodesWarn 向用户提示「是否输入出错」。
func TestParseMaxNodes(t *testing.T) {
	valid := []struct {
		name string
		body map[string]any
		want int
	}{
		{"缺省", map[string]any{}, 999},
		{"null", map[string]any{"maxNodes": nil}, 999},
		{"整数", map[string]any{"maxNodes": float64(7)}, 7},
		{"整数值浮点", map[string]any{"maxNodes": float64(25.0)}, 25},
		{"告警区间下边界", map[string]any{"maxNodes": float64(1000)}, 1000},
		{"硬上限边界", map[string]any{"maxNodes": float64(9999)}, 9999},
		{"硬上限整数值浮点", map[string]any{"maxNodes": float64(9999.0)}, 9999},
	}
	for _, c := range valid {
		got, err := parseMaxNodes(c.body)
		if err != nil || got != c.want {
			t.Errorf("%s: parseMaxNodes = %d, %v; want %d", c.name, got, err, c.want)
		}
	}
	invalid := []any{float64(0), float64(-5), float64(25.1), "25", true,
		float64(10000), float64(99999), float64(1e20), float64(9999.5)}
	for _, v := range invalid {
		if _, err := parseMaxNodes(map[string]any{"maxNodes": v}); err == nil {
			t.Errorf("maxNodes=%v 应报错", v)
		} else if !strings.Contains(err.Error(), "999") {
			t.Errorf("maxNodes=%v 报错应说明正确输入（缺省 999）: %s", v, err.Error())
		}
	}
	// 超硬上限的报错须带「是否输入出错」提示
	if _, err := parseMaxNodes(map[string]any{"maxNodes": float64(10000)}); err == nil ||
		!strings.Contains(err.Error(), "是否输入出错") {
		t.Errorf("maxNodes=10000 报错应含「是否输入出错」: %v", err)
	}
}

// maxNodesWarn 软上限提示：≤999 静默，>999 给「是否输入出错」文案。
func TestMaxNodesWarn(t *testing.T) {
	for _, n := range []int{1, 25, 999} {
		if w := maxNodesWarn(n); w != "" {
			t.Errorf("maxNodesWarn(%d) = %q，应静默", n, w)
		}
	}
	for _, n := range []int{1000, 5000, 9999} {
		w := maxNodesWarn(n)
		if !strings.Contains(w, "是否输入出错") || !strings.Contains(w, "9999") {
			t.Errorf("maxNodesWarn(%d) = %q，应含输错提示与硬上限", n, w)
		}
	}
}

// 告警区间的提示须到达用户：POST 响应 warning 字段 + job 日志首行
// （nodes/test 走缓存命中路径也要记）。>9999 连 job 都不创建（400）。
func TestMaxNodesWarningSurfaces(t *testing.T) {
	server, _, handler := newTestServer(t)
	token := loginToken(t, handler)

	// 预热缓存使 nodes/test 秒回（不拉外网）；损坏 models.json 使 update
	// 立即失败——只验证响应与 job 日志携带 warning，不依赖任务结果。
	sub := "https://example.invalid/sub"
	server.nodeCache.MarkTestDone(sub, []proxypool.NodeTestResult{{ID: "n-0", Kind: "vless-reality", OK: true}})
	if err := os.WriteFile(server.modelsPath(), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path, body string
	}{
		{"nodes/test 告警区间（缓存命中）", "/api/nodes/test", fmt.Sprintf(`{"url":%q,"maxNodes":1500}`, sub)},
		{"pricing/update 硬上限边界", "/api/pricing/update", `{"maxNodes":9999}`},
	} {
		rec := performRequest(handler, http.MethodPost, tc.path, tc.body, token)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d; body = %s", tc.name, rec.Code, rec.Body.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		warn, _ := resp["warning"].(string)
		if !strings.Contains(warn, "是否输入出错") || !strings.Contains(warn, "9999") {
			t.Fatalf("%s warning = %q", tc.name, warn)
		}
		jobID, _ := resp["jobId"].(string)
		waitForJobs(t, server)
		jobRec := performRequest(handler, http.MethodGet, "/api/jobs/"+jobID, "", token)
		var snap job
		if err := json.Unmarshal(jobRec.Body.Bytes(), &snap); err != nil {
			t.Fatal(err)
		}
		if len(snap.Log) == 0 || !strings.Contains(snap.Log[0], "是否输入出错") {
			t.Fatalf("%s job log[0] = %v，应含警告", tc.name, snap.Log)
		}
	}

	// 常用区间内不带 warning 字段；超硬上限 400 且不创建 job。
	rec := performRequest(handler, http.MethodPost, "/api/nodes/test",
		fmt.Sprintf(`{"url":%q,"maxNodes":999}`, sub), token)
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if _, has := resp["warning"]; has {
		t.Fatalf("maxNodes=999 不应带 warning: %s", rec.Body.String())
	}
	waitForJobs(t, server)
	before := jobCount(server)
	over := performRequest(handler, http.MethodPost, "/api/nodes/test",
		fmt.Sprintf(`{"url":%q,"maxNodes":10000}`, sub), token)
	if over.Code != http.StatusBadRequest {
		t.Fatalf("maxNodes=10000 status = %d，应 400", over.Code)
	}
	requireErrorJSON(t, over)
	if got := jobCount(server); got != before {
		t.Fatalf("超硬上限请求创建了 job（%d → %d）", before, got)
	}
}

func TestFXCacheValidation(t *testing.T) {
	server, _, handler := newTestServer(t)
	token := loginToken(t, handler)
	if err := os.WriteFile(server.fxPath(), []byte(`{"ts":`+fmt.Sprint(time.Now().Unix())+
		`,"data":{"rate":7.2,"source":"cached","fetchedAt":"today"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := performRequest(handler, http.MethodGet, "/api/fx", "", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid cache status = %d; body = %s", rec.Code, rec.Body.String())
	}
	var data map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if data["rate"] != 7.2 || data["cached"] != true {
		t.Fatalf("valid cache not returned: %v", data)
	}
	for _, rate := range []any{nil, true, "7.2", float64(0), -1, math.NaN(), math.Inf(1)} {
		if validFXData(map[string]any{"rate": rate}) {
			t.Errorf("invalid FX rate accepted: %v", rate)
		}
	}
	if !validFXData(map[string]any{"rate": 7.2}) {
		t.Fatal("positive finite FX rate rejected")
	}
}
