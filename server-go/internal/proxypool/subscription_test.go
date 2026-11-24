package proxypool

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const subBody = "vless://a1b2c3d4-0000-1111-2222-333344445555@1.2.3.4:443" +
	"?security=reality&pbk=PUBKEY&sid=ab12&sni=www.microsoft.com&fp=chrome&type=tcp&flow=xtls-rprx-vision#HK-VLESS\n" +
	"hysteria2://p%40ss%3Aw0rd@5.6.7.8:443?sni=hy2.example.com&insecure=1" +
	"&mport=443-449,500&obfs=salamander&obfs-password=obfspw#SG-HY2\n" +
	"ss://unsupported@9.9.9.9:8388#SS-NODE\n"

// 订阅体解码：四种 base64 变体 + 明文都要解出同一份 URI 列表，
// 且显式报告命中路径。
func TestDecodeSubscriptionVariants(t *testing.T) {
	for name, enc := range map[string]func(string) string{
		"std":    func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) },
		"rawstd": func(s string) string { return base64.RawStdEncoding.EncodeToString([]byte(s)) },
		"urlsafe": func(s string) string {
			return base64.RawURLEncoding.EncodeToString([]byte(s))
		},
		"plain": func(s string) string { return s },
	} {
		text, how := decodeSubscription(enc(subBody))
		if !strings.Contains(text, "vless://") || !strings.Contains(text, "hysteria2://") {
			t.Errorf("%s 解码未得到 URI 列表（how=%s）", name, how)
		}
		if name == "plain" && !strings.Contains(how, "明文") {
			t.Errorf("明文应显式报告未解码，how=%s", how)
		}
	}
}

// vless+reality URI → sing-box outbound 字段逐项核对（含 userinfo %XX 解码）。
func TestParseVLESS(t *testing.T) {
	n, err := parseNodeURI("vless://a1b2c3d4-0000-1111-2222-333344445555@1.2.3.4:443" +
		"?security=reality&pbk=PUBKEY&sid=ab12&sni=www.microsoft.com&fp=firefox" +
		"&type=ws&path=%2Fapi&host=cdn.example.com&flow=xtls-rprx-vision#HK-01")
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind != "vless-reality" || n.Name != "HK-01" {
		t.Fatalf("kind/name 不符: %+v", n)
	}
	o := n.Outbound
	if o["server"] != "1.2.3.4" || o["server_port"] != 443 {
		t.Errorf("server/port 不符: %v %v", o["server"], o["server_port"])
	}
	if o["uuid"] != "a1b2c3d4-0000-1111-2222-333344445555" {
		t.Errorf("uuid 不符: %v", o["uuid"])
	}
	tls := o["tls"].(map[string]any)
	re := tls["reality"].(map[string]any)
	if re["public_key"] != "PUBKEY" || re["short_id"] != "ab12" {
		t.Errorf("reality 字段不符: %v", re)
	}
	if tls["server_name"] != "www.microsoft.com" {
		t.Errorf("sni 不符: %v", tls["server_name"])
	}
	if tls["utls"].(map[string]any)["fingerprint"] != "firefox" {
		t.Errorf("fp 不符: %v", tls["utls"])
	}
	tr := o["transport"].(map[string]any)
	if tr["type"] != "ws" || tr["path"] != "/api" ||
		tr["headers"].(map[string]any)["Host"] != "cdn.example.com" {
		t.Errorf("ws transport 不符: %v", tr)
	}
	if o["flow"] != "xtls-rprx-vision" {
		t.Errorf("flow 不符: %v", o["flow"])
	}
}

// hysteria2 URI → outbound：密码 %XX 解码、端口跳跃、obfs、insecure。
func TestParseHY2(t *testing.T) {
	n, err := parseNodeURI("hysteria2://p%40ss%3Aw0rd@5.6.7.8:443?sni=hy2.example.com" +
		"&insecure=1&mport=443-449,500&obfs=salamander&obfs-password=obfspw#SG")
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind != "hysteria2" {
		t.Fatalf("kind 不符: %v", n.Kind)
	}
	o := n.Outbound
	if o["password"] != "p@ss:w0rd" { // %40→@ %3A→: 显式解码
		t.Errorf("密码百分号解码不符: %v", o["password"])
	}
	tls := o["tls"].(map[string]any)
	if tls["insecure"] != true || tls["server_name"] != "hy2.example.com" {
		t.Errorf("tls 不符: %v", tls)
	}
	ports := o["server_ports"].([]any)
	if len(ports) != 2 || ports[0] != "443:449" || ports[1] != "500" {
		t.Errorf("端口跳跃解析不符: %v", ports)
	}
	obfs := o["obfs"].(map[string]any)
	if obfs["type"] != "salamander" || obfs["password"] != "obfspw" {
		t.Errorf("obfs 不符: %v", obfs)
	}
}

// 拒绝路径：非 reality vless、未知 scheme、坏端口——显式报错不静默。
func TestParseNodeURIRejects(t *testing.T) {
	for _, u := range []string{
		"vless://uuid@1.2.3.4:443?security=tls#x",     // 非 reality
		"ss://method:pass@1.2.3.4:8388#x",             // 未知 scheme
		"vless://uuid@1.2.3.4:notaport?security=reality&pbk=k#x",
		"hysteria2://pw@1.2.3.4:0#x",
	} {
		if _, err := parseNodeURI(u); err == nil {
			t.Errorf("应拒绝但未拒绝: %s", u)
		}
	}
}

// LoadSubscription 端到端：httptest 服务返回 base64 订阅体 → 解析出 2 节点
// （ss 行跳过），vless-reality 排前。
func TestLoadSubscription(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(base64.StdEncoding.EncodeToString([]byte(subBody))))
	}))
	defer srv.Close()
	nodes, err := LoadSubscription(srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("节点数不符: %d", len(nodes))
	}
	if nodes[0].Kind != "vless-reality" || nodes[1].Kind != "hysteria2" {
		t.Errorf("排序不符: %v / %v", nodes[0].Kind, nodes[1].Kind)
	}
}
