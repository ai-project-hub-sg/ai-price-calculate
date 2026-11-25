// marshal.go — models.json 写回策略：diff 最小化。
// 顶层 meta 与未被 merge 的 provider 子树原样保留字节（RawMessage 直通），
// 只有被 merge 的 provider 走 canonical-order 序列化
// （对齐 Python json.dump(indent=2, ensure_ascii=False)，字段序固定）。
package fetch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// Dataset models.json 的读写视图：raw 子树 + 按需解析 + dirty 追踪。
type Dataset struct {
	rootOrder []string
	extra     map[string]json.RawMessage // 非 meta/providers 的顶层键（保留原样）
	meta      json.RawMessage
	provOrder []string
	rawProv   map[string]json.RawMessage // id → 原始子树字节
	provs     map[string]map[string]any  // id → 已解析（dirty 才写）
	dirty     map[string]bool
	eol       string // 输入文件换行风格，写回保留（\n 或 \r\n）
}

// LoadDataset 读 models.json：顶层与 provider 子树保留原始字节。
func LoadDataset(path string) (*Dataset, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, err
	}
	d := &Dataset{
		extra:   map[string]json.RawMessage{},
		rawProv: map[string]json.RawMessage{},
		provs:   map[string]map[string]any{},
		dirty:   map[string]bool{},
		eol:     "\n",
	}
	if bytes.Contains(raw, []byte("\r\n")) {
		// 保留输入换行风格：Windows 上 Python open("w") 产出 CRLF，
		// 写回不引入整文件换行翻转噪音
		d.eol = "\r\n"
	}
	// 顶层键序：按字节中出现顺序（json.Decoder token 流）
	d.rootOrder = topLevelKeys(raw)
	for k, v := range top {
		switch k {
		case "meta":
			d.meta = v
		case "providers":
			var pl []json.RawMessage
			if err := json.Unmarshal(v, &pl); err != nil {
				return nil, fmt.Errorf("providers 解析失败: %w", err)
			}
			if pl == nil {
				return nil, fmt.Errorf("providers 必须是数组")
			}
			for _, pr := range pl {
				var id struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(pr, &id); err != nil || id.ID == "" {
					return nil, fmt.Errorf("provider 条目缺 id")
				}
				d.provOrder = append(d.provOrder, id.ID)
				d.rawProv[id.ID] = pr
			}
		default:
			d.extra[k] = v
		}
	}
	if _, ok := top["providers"]; !ok {
		return nil, fmt.Errorf("models.json 缺少 providers 数组")
	}
	return d, nil
}

// topLevelKeys 用 token 流取顶层对象键序（JSON 键序有语义价值：review diff）。
func topLevelKeys(raw []byte) []string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var keys []string
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return keys
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			break
		}
		if s, ok := t.(string); ok {
			keys = append(keys, s)
			var skip json.RawMessage
			dec.Decode(&skip) // 跳过对应值
		}
	}
	return keys
}

// Provider 返回 provider 的已解析视图（首次访问时解析 raw，标记 dirty）。
// 合并代码只应通过本方法取得可写副本。
func (d *Dataset) Provider(id string) map[string]any {
	if p, ok := d.provs[id]; ok {
		return p
	}
	raw, ok := d.rawProv[id]
	if !ok {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	d.provs[id] = m
	d.dirty[id] = true
	return m
}

// Marshal 写回：meta 与未 dirty provider 原样；dirty provider canonical 序。
func (d *Dataset) Marshal() ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("{")
	b.WriteString(d.eol)
	first := true
	keys := append([]string{}, d.rootOrder...)
	for _, k := range []string{"meta", "providers"} { // 兜底：顺序表缺失时补上
		found := false
		for _, x := range keys {
			if x == k {
				found = true
			}
		}
		if !found {
			keys = append(keys, k)
		}
	}
	for _, k := range keys {
		var val []byte
		var err error
		switch k {
		case "meta":
			val = d.meta
		case "providers":
			val, err = d.marshalProviders()
		default:
			if v, ok := d.extra[k]; ok {
				val = v
			} else {
				continue
			}
		}
		if err != nil {
			return nil, err
		}
		if len(val) == 0 {
			val = []byte("null")
		}
		if !first {
			b.WriteString(",")
			b.WriteString(d.eol)
		}
		first = false
		b.WriteString("  ")
		writeJSONString(&b, k)
		b.WriteString(": ")
		b.Write(val) // raw 子树在原文件中已是正确绝对缩进，原样直通
	}
	b.WriteString(d.eol)
	b.WriteString("}")
	return b.Bytes(), nil
}

func (d *Dataset) marshalProviders() ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("[")
	b.WriteString(d.eol)
	for i, id := range d.provOrder {
		if i > 0 {
			b.WriteString(",")
			b.WriteString(d.eol)
		}
		if d.dirty[id] {
			// dirty：canonical 输出（depth-0）整体右移到数组元素位置（depth 2）
			out, err := MarshalCanonical(d.provs[id])
			if err != nil {
				return nil, err
			}
			b.Write(indentBlock(out, 2, d.eol))
		} else {
			// 未动：原始字节直通。raw 子串从 "{" 起，首行需补 depth-2 缩进，其余行已带正确绝对缩进。
			b.WriteString("    ")
			b.Write(d.rawProv[id])
		}
	}
	b.WriteString(d.eol)
	b.WriteString("  ]")
	return b.Bytes(), nil
}

// indentBlock 把 depth-0 的格式化 JSON 文本整体右移 depth 级（全部行），
// 行尾统一为 eol（canonical 输出内部是 LF，JSON 字符串值不含字面换行，可安全替换）。
func indentBlock(raw []byte, depth int, eol string) []byte {
	pad := strings.Repeat("  ", depth)
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "" {
			lines[i] = pad + l
		}
	}
	return []byte(strings.Join(lines, eol))
}

var fieldOrders = map[string][]string{
	"provider": {"id", "name", "currency", "source", "checkedAt", "officialUpdatedAt", "autoFetch", "autoFetchReason", "autoFetchUrl", "notes", "models"},
	"model":    {"id", "name", "contextWindow", "status", "fetchedAt", "officialUpdatedAt", "offPeakRatio", "tiers", "notes", "peakWindows", "peakNote"},
	"tier":     {"upToTokens", "input", "output", "cacheRead", "cacheWrites"},
	"cacheW":   {"ttl", "price"},
	"meta":     {"version", "description", "tierBasis", "fields", "disclaimer"},
	"fields":   {"upToTokens", "input", "output", "cacheRead", "cacheWrites", "offPeakRatio", "fetchedAt", "officialUpdatedAt"},
}

func shapeOf(m map[string]any) []string {
	switch {
	case m["tierBasis"] != nil || m["fields"] != nil:
		return fieldOrders["meta"]
	case m["models"] != nil:
		return fieldOrders["provider"]
	case m["tiers"] != nil:
		return fieldOrders["model"]
	case m["ttl"] != nil && m["price"] != nil:
		return fieldOrders["cacheW"]
	case m["input"] != nil || m["output"] != nil || m["upToTokens"] != nil:
		if _, ok := m["upToTokens"].(string); ok {
			return fieldOrders["fields"] // meta.fields 文档表（全字符串值）
		}
		return fieldOrders["tier"]
	}
	return nil
}

// MarshalCanonical canonical-order 序列化（indent=2、UTF-8 原文不转义）。
func MarshalCanonical(v any) ([]byte, error) {
	var b bytes.Buffer
	if err := writeValue(&b, v, 0); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func orderedKeys(m map[string]any) []string {
	canon := shapeOf(m)
	seen := make(map[string]bool, len(canon))
	keys := make([]string, 0, len(m))
	for _, k := range canon {
		if _, ok := m[k]; ok {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	var extra []string
	for k := range m {
		if !seen[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	return append(keys, extra...)
}

func writeValue(b *bytes.Buffer, v any, depth int) error {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case map[string]any:
		return writeMap(b, t, depth)
	case string:
		writeJSONString(b, t)
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case float64, float32, int, int64, int32, uint64:
		return writeNumber(b, t)
	default:
		rv := reflect.ValueOf(v)
		if rv.IsValid() && (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array) {
			return writeSlice(b, rv, depth)
		}
		return fmt.Errorf("unsupported JSON value type %T", v)
	}
	return nil
}

func writeNumber(b *bytes.Buffer, v any) error {
	var raw string
	switch n := v.(type) {
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return fmt.Errorf("unsupported JSON number %v", n)
		}
		raw = strconv.FormatFloat(n, 'f', -1, 64)
		if !strings.Contains(raw, ".") {
			raw += ".0"
		}
	case float32:
		if math.IsNaN(float64(n)) || math.IsInf(float64(n), 0) {
			return fmt.Errorf("unsupported JSON number %v", n)
		}
		raw = strconv.FormatFloat(float64(n), 'f', -1, 32)
		if !strings.Contains(raw, ".") {
			raw += ".0"
		}
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return err
		}
		raw = string(encoded)
	}
	b.WriteString(raw)
	return nil
}

func writeMap(b *bytes.Buffer, m map[string]any, depth int) error {
	keys := orderedKeys(m)
	if len(keys) == 0 {
		b.WriteString("{}")
		return nil
	}
	b.WriteString("{\n")
	for i, k := range keys {
		b.WriteString(strings.Repeat("  ", depth+1))
		writeJSONString(b, k)
		b.WriteString(": ")
		if err := writeValue(b, m[k], depth+1); err != nil {
			return err
		}
		if i < len(keys)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString(strings.Repeat("  ", depth))
	b.WriteByte('}')
	return nil
}

func writeSlice(b *bytes.Buffer, rv reflect.Value, depth int) error {
	if rv.Len() == 0 {
		b.WriteString("[]")
		return nil
	}
	b.WriteString("[\n")
	for i := 0; i < rv.Len(); i++ {
		b.WriteString(strings.Repeat("  ", depth+1))
		if err := writeValue(b, rv.Index(i).Interface(), depth+1); err != nil {
			return err
		}
		if i < rv.Len()-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString(strings.Repeat("  ", depth))
	b.WriteByte(']')
	return nil
}

// writeJSONString 对齐 Python json.dumps(ensure_ascii=False)：只转义
// 引号、反斜杠与控制字符，UTF-8 原文输出，不做 HTML 转义。
func writeJSONString(b *bytes.Buffer, s string) {
	const hex = "0123456789abcdef"
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte(hex[byte(r)>>4])
				b.WriteByte(hex[byte(r)&0xf])
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}
