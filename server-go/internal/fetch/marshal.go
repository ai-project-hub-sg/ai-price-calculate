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
// 解码保留 Python 数字类型语义：整数词法（64000）→ int64，小数/指数
// 词法（2.0、1e-7）→ float64——encoding/json 默认全解 float64 会丢失
// int/float 区分，写回时无论怎么格式化都必然错一侧（64000.0 或 2 而非 2.0）。
func (d *Dataset) Provider(id string) map[string]any {
	if p, ok := d.provs[id]; ok {
		return p
	}
	raw, ok := d.rawProv[id]
	if !ok {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil
	}
	normalizeNumbers(m)
	d.provs[id] = m
	d.dirty[id] = true
	return m
}

// normalizeNumbers 把 UseNumber 解出的 json.Number 按 Python json.loads
// 规则分流：无小数点/指数的词法 → int64，否则 → float64。递归原地改写。
func normalizeNumbers(v any) any {
	switch t := v.(type) {
	case json.Number:
		s := string(t)
		if isIntLexeme(s) {
			if i, err := strconv.ParseInt(s, 10, 64); err == nil {
				return i
			}
			// 超 int64：Python int 任意精度，Go 无内建——保留 json.Number
			// 原词法直通（Marshal 原样写出），字节与 Python 一致、零精度损失
			return t
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
		return t // 防御：解码器已保证合法数字；原样直通交 Marshal 校验
	case map[string]any:
		for k, val := range t {
			t[k] = normalizeNumbers(val)
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = normalizeNumbers(val)
		}
		return t
	default:
		return v
	}
}

// isIntLexeme Python int 词法：可选负号 + 纯数字，无小数点无指数。
func isIntLexeme(s string) bool {
	for i, r := range s {
		if i == 0 && r == '-' {
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
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
	case json.Number:
		// 超 int64 整数等保留的原词法：Marshal 校验合法性后原样写出，
		// 与 Python 任意精度 int 的输出字节一致。
		raw, err := json.Marshal(t)
		if err != nil {
			return err
		}
		b.Write(raw)
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
	switch n := v.(type) {
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return fmt.Errorf("unsupported JSON number %v", n)
		}
		b.WriteString(pyFloatRepr(n))
	case float32:
		if math.IsNaN(float64(n)) || math.IsInf(float64(n), 0) {
			return fmt.Errorf("unsupported JSON number %v", n)
		}
		b.WriteString(pyFloatRepr(float64(n)))
	case int:
		b.WriteString(strconv.Itoa(n))
	case int64:
		b.WriteString(strconv.FormatInt(n, 10))
	case int32:
		b.WriteString(strconv.FormatInt(int64(n), 10))
	case uint64:
		b.WriteString(strconv.FormatUint(n, 10))
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return err
		}
		b.Write(encoded)
	}
	return nil
}

// pyFloatRepr 按 CPython repr/json.dumps 规则格式化 float64：
// 最短回退数字（Go 与 CPython 相同）+ 指数 < -4 或 >= 16 用科学计数法
// （指数带符号、至少两位补零），其余十进制定点、integral 补 ".0"。
// 例：2.0→"2.0" 64000.0→"64000.0" 0.0001→"0.0001" 1e-5→"1e-05"
// 1e15→"1000000000000000.0" 1e16→"1e+16"
func pyFloatRepr(f float64) string {
	if f == 0 {
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	// 'e' 格式精度 -1 产出最短回退数字，形如 [-]d[.ddd]e±xx，据此取数字与指数
	s := strconv.FormatFloat(f, 'e', -1, 64)
	neg := strings.HasPrefix(s, "-")
	body := strings.TrimPrefix(s, "-")
	eIdx := strings.IndexByte(body, 'e')
	mantissa := body[:eIdx]
	exp, _ := strconv.Atoi(body[eIdx+1:])
	digits := strings.Replace(mantissa, ".", "", 1)

	if exp < -4 || exp >= 16 {
		// 科学计数法：系数单数字时不补 .0（Python repr(1e16)='1e+16'）
		var coef string
		if len(digits) == 1 {
			coef = digits
		} else {
			coef = digits[:1] + "." + digits[1:]
		}
		sign := "+"
		ae := exp
		if ae < 0 {
			sign, ae = "-", -ae
		}
		es := strconv.Itoa(ae)
		if len(es) < 2 {
			es = "0" + es
		}
		out := coef + "e" + sign + es
		if neg {
			return "-" + out
		}
		return out
	}

	// 定点：digits 首位对应 10^exp
	var out string
	switch {
	case exp >= len(digits)-1: // 整数（可能补零），integral float 补 .0
		out = digits + strings.Repeat("0", exp-len(digits)+1) + ".0"
	case exp >= 0: // 小数点在数字内部
		out = digits[:exp+1] + "." + digits[exp+1:]
	default: // 0.00xx
		out = "0." + strings.Repeat("0", -exp-1) + digits
	}
	if neg {
		return "-" + out
	}
	return out
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
