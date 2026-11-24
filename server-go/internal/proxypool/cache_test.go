package proxypool

import (
	"testing"
	"time"
)

func testNode(name string) Node {
	return Node{ID: "sub-0", Name: name, Kind: "vless-reality",
		Outbound: map[string]any{"type": "vless", "server": "1.2.3.4", "uuid": "u-" + name}}
}

func TestCacheUsableAndTTL(t *testing.T) {
	c := NewCache(time.Hour)
	n1, n2 := testNode("a"), testNode("b")
	nodes := []Node{n1, n2}
	c.Mark("sub-1", n1, true, 300, "")
	c.Mark("sub-1", n2, false, 0, "timeout")

	got := c.Usable("sub-1", nodes)
	if len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("期望只有 a 可用，got %v", got)
	}
}

func TestCacheSubURLChangeInvalidates(t *testing.T) {
	c := NewCache(time.Hour)
	n := testNode("a")
	c.Mark("sub-1", n, true, 300, "")
	if got := c.Usable("sub-2", []Node{n}); len(got) != 0 {
		t.Fatalf("换订阅后缓存应整体失效，got %v", got)
	}
	// 新订阅写入后旧键不残留
	c.Mark("sub-2", n, false, 0, "x")
	c.Mark("sub-2", testNode("b"), true, 1, "")
	if got := c.Usable("sub-2", []Node{n}); len(got) != 0 {
		t.Fatalf("sub-2 键空间下 a 应为不可用，got %v", got)
	}
}

func TestCacheKeyStableAcrossRename(t *testing.T) {
	a := testNode("old-name")
	b := a
	b.Name = "new-name" // 改名不换 outbound → 同键
	if Key(a) != Key(b) {
		t.Fatal("同名改名不应换键")
	}
	c := a
	c.Outbound = map[string]any{"type": "vless", "server": "9.9.9.9"}
	if Key(a) == Key(c) {
		t.Fatal("换服务端点应换键")
	}
}

func TestCacheAbsorb(t *testing.T) {
	c := NewCache(time.Hour)
	okNode, deadNode := testNode("ok"), testNode("dead")
	reps := []ProbeReport{
		{Node: "ok-label", node: okNode, Results: map[string]Result{
			"x": {Status: 200, Ms: 300},
			"y": {Status: 0, Error: "timeout"},
		}},
		{Node: "dead-label", node: deadNode, Error: "启动失败"},
	}
	c.Absorb("sub-1", reps)
	got := c.Usable("sub-1", []Node{okNode, deadNode})
	if len(got) != 1 || got[0].Name != "ok" {
		t.Fatalf("Absorb 后应只有 ok 可用，got %v", got)
	}
}
