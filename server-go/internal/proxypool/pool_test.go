package proxypool

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchURLClosesIdleConnections(t *testing.T) {
	closed := make(chan struct{}, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			select {
			case closed <- struct{}{}:
			default:
			}
		}
	}
	srv.Start()
	defer srv.Close()

	result := FetchURL(srv.URL, 0, 1000)
	if result.Error != "" || result.Status != http.StatusOK {
		t.Fatalf("FetchURL = %+v", result)
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("idle connection was not closed")
	}
}

func TestTestNodesRecoversProbePanic(t *testing.T) {
	original := probeNode
	probeNode = func(Node, map[string]string, int, func(string, []byte) bool, *ProbeHooks) ProbeReport {
		panic("probe boom")
	}
	defer func() { probeNode = original }()

	nodes := []Node{{ID: "one", Kind: "hysteria2"}, {ID: "two", Kind: "hysteria2"}}
	results := TestNodes(nodes, "https://example.com", 0, 1, 100, nil, "", nil)
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	for _, result := range results {
		if result.OK || !strings.Contains(result.Error, "probe boom") {
			t.Errorf("panic was not recorded: %+v", result)
		}
	}
}
