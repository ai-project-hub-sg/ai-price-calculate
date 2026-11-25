// 订阅链接 → 节点列表：GET + base64 解码 + vless:// / hysteria2:// URI 解析。
// 取代 Python 版对 v2rayN guiNDB.db 的依赖——URI query 携带同样字段。
package proxypool

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

type Node struct {
	ID       string         // sub-<n>
	Name     string         // URI fragment（备注名）
	Kind     string         // "vless-reality" | "hysteria2"
	Outbound map[string]any // sing-box outbound JSON（tag 占位 "out"）
}

// ValidateSubscriptionURL 与 Python server/validation.py validate_proxy_url
// 等价：先 strip 再校验，返回规范化地址供调用方使用/存储。
func ValidateSubscriptionURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("代理订阅 URL 不能为空")
	}
	if len(raw) > 4096 || strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
		return "", fmt.Errorf("代理订阅 URL 格式错误")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Hostname() == "" ||
		(strings.ToLower(u.Scheme) != "http" && strings.ToLower(u.Scheme) != "https") {
		return "", fmt.Errorf("代理订阅 URL 格式错误：仅支持完整的 http/https 地址")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("代理订阅 URL 格式错误：端口无效")
		}
	}
	return raw, nil
}

func (n Node) Label() string {
	if n.Name != "" {
		return n.Kind + ":" + n.Name
	}
	return n.Kind + ":" + n.ID
}

// LoadSubscription 拉取并解析订阅链接，返回支持的节点（vless+reality / hysteria2）。
// log 非空时输出显式路径决策（解码方式、节点统计）。
// 注意：订阅地址本身必须直连可达——先有鸡还是先有蛋，它不走路径代理。
func LoadSubscription(subURL string, log func(string)) ([]Node, error) {
	var err error
	if subURL, err = ValidateSubscriptionURL(subURL); err != nil {
		return nil, err
	}
	// 订阅端点常见抖动（头部超时 / 200 后 body 滴漏卡死）——失败自动重试一次，
	// 两次都失败才报错。重试日志显式可见。
	var r Result
	for attempt := 1; ; attempt++ {
		r = FetchURL(subURL, 0, 15_000)
		if r.Error == "" && r.Status == 200 {
			break
		}
		if attempt == 2 {
			return nil, fmt.Errorf("订阅不可达 status=%d err=%s", r.Status, r.Error)
		}
		if log != nil {
			log(fmt.Sprintf("订阅拉取失败（status=%d err=%s），自动重试…", r.Status, r.Error))
		}
	}
	text, how := decodeSubscription(string(r.Body))
	if log != nil {
		log(fmt.Sprintf("订阅获取 %dB，解码方式：%s", r.Bytes, how))
	}
	var nodes []Node
	skipped := 0
	for _, line := range strings.Split(text, "\n") {
		u := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if !strings.Contains(u, "://") {
			continue
		}
		n, err := parseNodeURI(u)
		if err != nil {
			skipped++
			continue
		}
		n.ID = fmt.Sprintf("sub-%d", len(nodes))
		nodes = append(nodes, *n)
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("订阅解析出 0 个可用节点（跳过 %d 行）", skipped)
	}
	// 与 Python 一致：vless-reality 组在前
	first, rest := []Node{}, []Node{}
	for _, n := range nodes {
		if n.Kind == "vless-reality" {
			first = append(first, n)
		} else {
			rest = append(rest, n)
		}
	}
	nodes = append(first, rest...)
	if log != nil {
		log(fmt.Sprintf("节点 %d 个（vless-reality %d，hysteria2 %d），跳过不支持 %d 行",
			len(nodes), len(first), len(rest), skipped))
	}
	return nodes, nil
}

// decodeSubscription 订阅体 = 每行一个 URI 的 base64。
// 显式返回解码路径（"base64" / "明文"），不静默猜测。
func decodeSubscription(body string) (text, how string) {
	compact := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, body)
	if b, err := base64.StdEncoding.DecodeString(compact); err == nil {
		s := string(b)
		if strings.Contains(s, "://") {
			return s, "base64(标准,带padding)"
		}
	}
	// 无 padding / URL 安全变体
	for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(compact); err == nil {
			s := string(b)
			if strings.Contains(s, "://") {
				return s, "base64(无padding/URL安全)"
			}
		}
	}
	return body, "明文（未解码）"
}

func parseNodeURI(u string) (*Node, error) {
	scheme, rest, ok := strings.Cut(u, "://")
	if !ok {
		return nil, fmt.Errorf("bad uri")
	}
	scheme = strings.ToLower(scheme)
	switch scheme {
	case "vless":
		return parseVLESS(rest, u)
	case "hysteria2", "hy2":
		return parseHY2(rest, u)
	}
	return nil, fmt.Errorf("unsupported scheme %s", scheme)
}

func parseVLESS(rest, raw string) (*Node, error) {
	pu, err := url.Parse("vless://" + rest)
	if err != nil {
		return nil, err
	}
	q := pu.Query()
	if !strings.EqualFold(q.Get("security"), "reality") {
		return nil, fmt.Errorf("vless 非 reality（security=%q）", q.Get("security"))
	}
	port, err := strconv.Atoi(pu.Port())
	if err != nil || port <= 0 {
		return nil, fmt.Errorf("bad port")
	}
	uuid, _ := url.QueryUnescape(pu.User.Username()) // userinfo 可能带 %XX，显式解码
	out := map[string]any{
		"type": "vless", "tag": "out",
		"server": pu.Hostname(), "server_port": port,
		"uuid": uuid,
		"tls": map[string]any{
			"enabled": true, "server_name": q.Get("sni"),
			"utls":    map[string]any{"enabled": true, "fingerprint": orDefault(q.Get("fp"), "chrome")},
			"reality": map[string]any{"enabled": true, "public_key": q.Get("pbk"), "short_id": q.Get("sid")},
		},
	}
	if f := q.Get("flow"); f != "" {
		out["flow"] = f
	}
	tr, err := vlessTransport(q)
	if err != nil {
		return nil, err
	}
	if tr != nil {
		out["transport"] = tr
	}
	return &Node{Kind: "vless-reality", Name: pu.Fragment, Outbound: out}, nil
}

func vlessTransport(q url.Values) (map[string]any, error) {
	net := strings.ToLower(q.Get("type"))
	switch net {
	case "", "tcp":
		if ht := q.Get("headerType"); ht != "" && ht != "none" {
			return nil, fmt.Errorf("tcp headerType=%s 不支持", ht)
		}
		return nil, nil
	case "ws":
		return map[string]any{
			"type": "ws", "path": orDefault(q.Get("path"), "/"),
			"headers": map[string]any{"Host": q.Get("host")}}, nil
	case "grpc":
		return map[string]any{"type": "grpc", "service_name": q.Get("serviceName")}, nil
	case "httpupgrade":
		return map[string]any{"type": "httpupgrade",
			"host": q.Get("host"), "path": orDefault(q.Get("path"), "/")}, nil
	}
	return nil, fmt.Errorf("vless transport type=%s 不支持", net)
}

func parseHY2(rest, raw string) (*Node, error) {
	pu, err := url.Parse("hy2://" + rest)
	if err != nil {
		return nil, err
	}
	q := pu.Query()
	port, err := strconv.Atoi(pu.Port())
	if err != nil || port <= 0 {
		return nil, fmt.Errorf("bad port")
	}
	pw, _ := url.QueryUnescape(pu.User.Username()) // 密码可能含 %XX 编码字符，显式解码
	out := map[string]any{
		"type": "hysteria2", "tag": "out",
		"server": pu.Hostname(), "server_port": port,
		"password": pw,
		"tls": map[string]any{
			"enabled": true, "server_name": q.Get("sni"),
			"insecure": truthy(q.Get("insecure")) || truthy(q.Get("allowInsecure")),
		},
	}
	if mport := q.Get("mport"); mport != "" { // 端口跳跃 "443-449,500" → "443:449","500"
		var ports []any
		for _, p := range strings.Split(mport, ",") {
			if p = strings.TrimSpace(p); p != "" {
				ports = append(ports, strings.ReplaceAll(p, "-", ":"))
			}
		}
		if len(ports) > 0 {
			out["server_ports"] = ports
		}
	}
	if strings.EqualFold(q.Get("obfs"), "salamander") {
		out["obfs"] = map[string]any{"type": "salamander", "password": q.Get("obfs-password")}
	}
	return &Node{Kind: "hysteria2", Name: pu.Fragment, Outbound: out}, nil
}

func truthy(s string) bool {
	switch strings.ToLower(s) {
	case "1", "true", "yes":
		return true
	}
	return false
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
