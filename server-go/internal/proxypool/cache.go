// 节点可用性缓存：探测一次的结果 1h 内复用，避免「刚测完节点、点更新又全池
// 重探」的浪费。进程内内存态（服务器重启即冷，代价可接受，不落盘）。
// 键空间按订阅地址隔离——换订阅即整表失效。
package proxypool

import (
	"encoding/json"
	"sync"
	"time"
)

type nodeStatus struct {
	ok    bool
	ms    int
	err   string
	at    time.Time
}

type Cache struct {
	mu       sync.Mutex
	ttl      time.Duration
	subURL   string
	subNodes []Node // 已解析节点列表（订阅本身也可能拉取失败/超时，缓存兜底）
	subAt    time.Time
	nodes    map[string]nodeStatus // key = Key(node)
}

func NewCache(ttl time.Duration) *Cache {
	return &Cache{ttl: ttl, nodes: map[string]nodeStatus{}}
}

// SubNodes 返回 TTL 内缓存的已解析节点列表（免重复拉订阅）；无/过期返回 nil。
func (c *Cache) SubNodes(subURL string) []Node {
	c.mu.Lock()
	defer c.mu.Unlock()
	if subURL == "" || subURL != c.subURL || time.Since(c.subAt) >= c.ttl {
		return nil
	}
	return append([]Node(nil), c.subNodes...)
}

// StaleSubNodes 无视 TTL 返回最近一次的节点列表——订阅拉取失败时的兜底。
func (c *Cache) StaleSubNodes(subURL string) []Node {
	c.mu.Lock()
	defer c.mu.Unlock()
	if subURL == "" || subURL != c.subURL {
		return nil
	}
	return append([]Node(nil), c.subNodes...)
}

// MarkSub 记录本次拉取成功的节点列表。
func (c *Cache) MarkSub(subURL string, nodes []Node) {
	if subURL == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.subURL != subURL {
		c.subURL = subURL
		c.nodes = map[string]nodeStatus{}
	}
	c.subNodes = append([]Node(nil), nodes...)
	c.subAt = time.Now()
}

// Key 节点稳定标识：outbound 配置序列化（map key 有序 → 确定性）。
// 节点改名/改序不换键，换订阅/换服务端点自然换键。
func Key(n Node) string {
	b, err := json.Marshal(n.Outbound)
	if err != nil {
		return n.Label()
	}
	return string(b)
}

// Usable 返回 nodes 中近期测试可用的节点（保持入参顺序）。
// subURL 与缓存记录的订阅不一致时缓存整体失效，返回空。
func (c *Cache) Usable(subURL string, nodes []Node) []Node {
	c.mu.Lock()
	defer c.mu.Unlock()
	if subURL == "" || subURL != c.subURL {
		return nil
	}
	out := []Node{}
	for _, n := range nodes {
		if s, ok := c.nodes[Key(n)]; ok && s.ok && time.Since(s.at) < c.ttl {
			out = append(out, n)
		}
	}
	return out
}

// Mark 写入单节点结果（ok=false 即从可用集剔除）。
func (c *Cache) Mark(subURL string, n Node, ok bool, ms int, errMsg string) {
	if subURL == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.subURL != subURL {
		c.subURL = subURL
		c.nodes = map[string]nodeStatus{}
		c.subNodes, c.subAt = nil, time.Time{}
	}
	c.nodes[Key(n)] = nodeStatus{ok: ok, ms: ms, err: errMsg, at: time.Now()}
}

// Absorb 批量吸收探测报告：节点成功启动且至少一个目标拿到 HTTP 响应
// （status>0，含被墙页——墙页也证明链路通）记可用；启动失败/全超时记不可用。
func (c *Cache) Absorb(subURL string, reps []ProbeReport) {
	for _, rep := range reps {
		if rep.Error != "" {
			c.Mark(subURL, rep.node, false, 0, rep.Error)
			continue
		}
		anyResp := false
		bestMs := 0
		for _, r := range rep.Results {
			if r.Status > 0 {
				anyResp = true
				if bestMs == 0 || r.Ms < bestMs {
					bestMs = r.Ms
				}
			}
		}
		if anyResp {
			c.Mark(subURL, rep.node, true, bestMs, "")
		} else {
			c.Mark(subURL, rep.node, false, 0, "全部目标无响应")
		}
	}
}
