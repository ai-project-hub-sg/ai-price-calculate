// 节点池：进程内 sing-box 起本地 mixed 端口 → 探测目标可用性。
// 与 Python nodepool.py 同流程，但核心从子进程换成内嵌库，零外部二进制。
package proxypool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	singjson "github.com/sagernet/sing/common/json"
)

const ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64)"
const maxFetchBodyBytes = 8 << 20

// baseCtx 全协议注册表上下文，进程内所有 box 实例共享。
var baseCtx = include.Context(context.Background())

type Result struct {
	Status int
	Bytes  int
	Ms     int
	Error  string
	Body   []byte
}

// FetchURL 显式抓取：proxyPort=0 → 严格直连（刻意不读 HTTPS_PROXY 等环境变量，
// 避免"直连"语义被隐式代理劫持）；proxyPort>0 → 本地 mixed 代理端口。
// 需要固定代理时应作为显式配置项，而不是隐式环境依赖。
func FetchURL(rawURL string, proxyPort int, timeoutMS int) Result {
	var proxy func(*http.Request) (*url.URL, error)
	if proxyPort > 0 {
		proxy = http.ProxyURL(&url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", proxyPort)})
	}
	client := &http.Client{
		Timeout:   time.Duration(timeoutMS) * time.Millisecond,
		Transport: &http.Transport{Proxy: proxy},
	}
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return Result{Error: "URL 格式错误: " + err.Error()}
	}
	req.Header.Set("User-Agent", ua)
	t0 := time.Now()
	resp, err := client.Do(req)
	ms := int(time.Since(t0).Milliseconds())
	if err != nil {
		return Result{Status: 0, Ms: ms, Error: err.Error()}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBodyBytes+1))
	if err != nil {
		return Result{Status: resp.StatusCode, Ms: ms, Error: err.Error()}
	}
	if len(body) > maxFetchBodyBytes {
		return Result{Status: resp.StatusCode, Ms: ms, Error: "响应体超过 8MB 上限"}
	}
	r := Result{Status: resp.StatusCode, Bytes: len(body), Ms: ms, Body: body}
	if resp.StatusCode != 200 {
		r.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return r
}

// LocalBox 单个节点临时本地代理（进程内 sing-box 实例）。
type LocalBox struct {
	Port int
	inst *box.Box
}

func StartNode(n Node) (*LocalBox, error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	cfg, err := json.Marshal(map[string]any{
		"log": map[string]any{"level": "error"},
		// 显式 DNS：缺省时 v1.14 DNS 客户端在并发交换下 nil 解引用崩进程
		// （panic 在 sing-box 自有 goroutine，业务层 recover 兜不住）
		"dns": map[string]any{
			"servers": []any{map[string]any{"type": "local", "tag": "local"}},
			"final":   "local",
		},
		"inbounds":  []any{map[string]any{"type": "mixed", "tag": "in", "listen": "127.0.0.1", "listen_port": port}},
		"outbounds": []any{n.Outbound},
	})
	if err != nil {
		return nil, err
	}
	var opt option.Options
	if err := singjson.UnmarshalContext(baseCtx, cfg, &opt); err != nil {
		return nil, fmt.Errorf("%s 配置解析失败: %w", n.Label(), err)
	}
	inst, err := box.New(box.Options{Options: opt, Context: baseCtx})
	if err != nil {
		return nil, fmt.Errorf("%s 实例创建失败: %w", n.Label(), err)
	}
	if err := inst.Start(); err != nil {
		inst.Close()
		return nil, fmt.Errorf("%s 启动失败: %w", n.Label(), err)
	}
	if !waitListen(port, 4*time.Second) {
		inst.Close()
		return nil, fmt.Errorf("%s 本地端口未就绪", n.Label())
	}
	return &LocalBox{Port: port, inst: inst}, nil
}

func (lb *LocalBox) Close() {
	if lb.inst != nil {
		lb.inst.Close()
	}
}

// freePort 让位后端口理论上可被抢（TOCTOU 小窗）——可接受：
// 若被抢，waitListen 等到的是别人的服务，探测抓取会失败并归因该节点，
// 结果只是误报节点不可用，不会产生错误流量。
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func waitListen(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
		if err == nil {
			c.Close()
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// ProbeReport 单节点探测结果（进 job log/report）。
type ProbeReport struct {
	Node    string            `json:"node"`
	Kind    string            `json:"kind"`
	Error   string            `json:"error,omitempty"`
	Results map[string]Result `json:"results"`
	node    Node              // 对应的 Node 对象（不进 JSON）
}

// ProbeHooks 探测事件回调（均可为nil）：
// OnNodeStart 节点开测；OnTarget/OnTargetDone 节点开始/结束抓某目标
// （组合进度 + 在飞集合）；OnWin 某目标被节点胜出瞬间；OnProgress 批次完成。
type ProbeHooks struct {
	OnNodeStart  func(label string, idx, total int)
	OnTarget     func(nodeLabel, key string)
	OnTargetDone func(nodeLabel, key string)
	OnWin        func(nodeLabel, key string)
	OnProgress   func(tested int, pending []string)
}

// ProbeNode 单节点探测一组 URL。validate 非nil时对 200 响应做内容校验
// （如"能解析出价目行"），校验不过记 error 不当可用——地区限制/反爬页
// 也是 200，只看状态码会误判节点可用。
func ProbeNode(n Node, urls map[string]string, timeoutMS int,
	validate func(key string, body []byte) bool, hooks *ProbeHooks) ProbeReport {
	out := ProbeReport{Node: n.Label(), Kind: n.Kind, node: n, Results: map[string]Result{}}
	lb, err := StartNode(n)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	defer lb.Close()
	for key, u := range urls {
		if hooks != nil && hooks.OnTarget != nil {
			hooks.OnTarget(n.Label(), key)
		}
		r := FetchURL(u, lb.Port, timeoutMS)
		if hooks != nil && hooks.OnTargetDone != nil {
			hooks.OnTargetDone(n.Label(), key)
		}
		if validate != nil && r.Status == 200 && !validate(key, r.Body) {
			r.Error = "返回内容校验未通过（疑似反爬/地区限制页）"
		}
		out.Results[key] = r // body 保留：胜出者的已校验内容直接复用，不再重抓
	}
	return out
}

// FetchThrough 用指定节点起代理并抓取完整内容。
func FetchThrough(n Node, rawURL string, timeoutMS int) Result {
	lb, err := StartNode(n)
	if err != nil {
		return Result{Status: 0, Error: err.Error()}
	}
	defer lb.Close()
	return FetchURL(rawURL, lb.Port, timeoutMS)
}

type Winner struct {
	Node  Node
	Label string
	Body  []byte // 探测时已校验的响应体，调用方直接复用（重抓有内容抖动风险）
}

type ProbeResult struct {
	Winners map[string]Winner
	Reports []ProbeReport
	Pending []string
}

// FindWorking 按候选顺序探测节点，为每个目标 URL 找首个可用节点（4 并发批）。
// hooks.OnNodeStart 每个节点开测时回调（label, 全局序号, 总候选数）——序号是
// "开测序"而非"完成序"。
func FindWorking(nodes []Node, urls map[string]string, maxNodes int, workers int, timeoutMS int,
	validate func(key string, body []byte) bool,
	hooks ProbeHooks) ProbeResult {
	if maxNodes > 0 && len(nodes) > maxNodes {
		nodes = nodes[:maxNodes]
	}
	if workers <= 0 {
		workers = 4
	}
	pending := map[string]string{}
	for k, v := range urls {
		pending[k] = v
	}
	out := ProbeResult{Winners: map[string]Winner{}}
	for i := 0; i < len(nodes) && len(pending) > 0; i += workers {
		end := i + workers
		if end > len(nodes) {
			end = len(nodes)
		}
		batch := nodes[i:end]
		targets := map[string]string{}
		for k, v := range pending {
			targets[k] = v
		}
		reps := make([]ProbeReport, len(batch))
		var wg sync.WaitGroup
		for j, n := range batch {
			if hooks.OnNodeStart != nil {
				hooks.OnNodeStart(n.Label(), i+j+1, len(nodes))
			}
			wg.Add(1)
			go func(j int, n Node) {
				defer wg.Done()
				defer func() {
					if r := recover(); r != nil {
						reps[j] = ProbeReport{Node: n.Label(), Kind: n.Kind, node: n,
							Error: fmt.Sprintf("panic: %v", r), Results: map[string]Result{}}
					}
				}()
				reps[j] = ProbeNode(n, targets, timeoutMS, validate, &hooks)
			}(j, n)
		}
		wg.Wait()
		for _, rep := range reps {
			out.Reports = append(out.Reports, rep)
			for key, r := range rep.Results {
				if _, ok := pending[key]; ok && r.Status == 200 && r.Error == "" {
					if _, won := out.Winners[key]; !won {
						out.Winners[key] = Winner{Node: rep.node, Label: rep.Node, Body: r.Body}
						if hooks.OnWin != nil {
							hooks.OnWin(rep.Node, key)
						}
					}
					delete(pending, key)
				}
			}
		}
		if hooks.OnProgress != nil {
			hooks.OnProgress(len(out.Reports), sortedKeys(pending))
		}
	}
	out.Pending = sortedKeys(pending)
	return out
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// NodeTestResult 「测试节点」端点结果：连通性探测（不取价目，只判能不能用）。
type NodeTestResult struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	OK    bool   `json:"ok"`
	Ms    int    `json:"ms"`
	Error string `json:"error,omitempty"`
}

// TestNodes 对全部节点（至多 maxNodes 个）做连通性探测：每个节点起本地
// sing-box 端口拉一次 probeURL，2xx 即判可用。与 FindWorking 不同——不为
// 特定目标找首个胜者，而是完整测一遍给出可用率，供「测试节点」按钮展示。
// 显式说明：探测目标固定为 probeURL（连通性），不代表一定能到某个价目页。
// cache 非nil时结果写入缓存（subURL 为键空间），供价目更新温路径直接复用。
func TestNodes(nodes []Node, probeURL string, maxNodes int, workers int, timeoutMS int,
	cache *Cache, subURL string,
	onProgress func(tested, total int)) []NodeTestResult {
	probed := nodes
	if maxNodes > 0 && len(probed) > maxNodes {
		probed = probed[:maxNodes]
	}
	if workers <= 0 {
		workers = 4
	}
	results := make([]NodeTestResult, len(probed))
	for i := 0; i < len(probed); i += workers {
		end := i + workers
		if end > len(probed) {
			end = len(probed)
		}
		var wg sync.WaitGroup
		for j := i; j < end; j++ {
			wg.Add(1)
			go func(j int, n Node) {
				defer wg.Done()
				rep := ProbeNode(n, map[string]string{"_": probeURL}, timeoutMS, nil, nil)
				r := NodeTestResult{ID: n.ID, Name: n.Label(), Kind: n.Kind}
				if rep.Error != "" {
					r.Error = rep.Error
				} else if res, ok := rep.Results["_"]; ok {
					r.Ms = res.Ms
					if res.Status >= 200 && res.Status < 300 {
						r.OK = true
					} else {
						r.Error = res.Error
						if r.Error == "" {
							r.Error = fmt.Sprintf("HTTP %d", res.Status)
						}
					}
				}
				results[j] = r
				if cache != nil {
					cache.Mark(subURL, n, r.OK, r.Ms, r.Error)
				}
			}(j, probed[j])
		}
		wg.Wait()
		if onProgress != nil {
			onProgress(end, len(probed))
		}
	}
	return results
}
