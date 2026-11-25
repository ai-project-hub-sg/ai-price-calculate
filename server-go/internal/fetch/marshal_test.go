package fetch

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// canonical marshal 的数字格式与 CPython repr/json.dumps 逐值一致
// （期望值由 Python 3 repr 实际生成）：integral float 补 .0、指数
// <-4 或 >=16 用科学计数法（两位补零）、其余定点。int64 永不带 .0——
// 类型保留解码保证 64000(int) 与 64000.0(float) 写回各自正确。
func TestMarshalCanonicalNumbers(t *testing.T) {
	floatCases := []struct {
		in   float64
		want string
	}{
		{2.0, "2.0"},
		{64000.0, "64000.0"},
		{0.006, "0.006"},
		{0.0000001, "1e-07"},
		{1e16, "1e+16"},
		{1e15, "1000000000000000.0"},
		{1050000.0, "1050000.0"},
		{0.0001, "0.0001"},
		{1e-5, "1e-05"},
		{0.30000000000000004, "0.30000000000000004"},
		{-2.5, "-2.5"},
		{0.0, "0.0"},
		{1.5e16, "1.5e+16"},
		{1e100, "1e+100"},
		{5e-324, "5e-324"},
		{0.07, "0.07"},
		{1234567890123456.0, "1234567890123456.0"},
	}
	for _, c := range floatCases {
		if got := pyFloatRepr(c.in); got != c.want {
			t.Errorf("pyFloatRepr(%v) = %s, want %s", c.in, got, c.want)
		}
	}
	// int64/int 按整数格式，永不带 .0（Python int 语义）
	out, err := MarshalCanonical(map[string]any{
		"contextWindow": int64(64000), "price": float64(2.0), "n": int(3),
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{`"contextWindow": 64000`, `"price": 2.0`, `"n": 3`} {
		if !strings.Contains(s, want) {
			t.Errorf("缺少 %s: %s", want, s)
		}
	}
	if strings.Contains(s, "64000.0") {
		t.Errorf("int64 不应带 .0: %s", s)
	}
}

// 解码类型保留：整数词法 → int64，小数/指数词法 → float64（Python json.loads 语义）。
// 这是写回字节对拍的前提——float64 单一类型数学上不可能同时写对 64000 与 2.0。
func TestNormalizeNumbersTypes(t *testing.T) {
	ds, err := LoadDataset(filepath.Join("testdata", "merge_input.json"))
	if err != nil {
		t.Fatal(err)
	}
	prov := ds.Provider("deepseek")
	if prov == nil {
		t.Fatal("deepseek provider 不存在")
	}
	var legacy map[string]any
	for _, m := range prov["models"].([]any) {
		mm := m.(map[string]any)
		if mm["id"] == "deepseek-legacy" {
			legacy = mm
		}
	}
	if legacy == nil {
		t.Fatal("deepseek-legacy 不存在")
	}
	if _, ok := legacy["contextWindow"].(int64); !ok {
		t.Errorf("contextWindow 64000 应为 int64，实际 %T", legacy["contextWindow"])
	}
	tiers := legacy["tiers"].([]any)
	tier0 := tiers[0].(map[string]any)
	if _, ok := tier0["input"].(float64); !ok {
		t.Errorf("input 1.0 应为 float64，实际 %T", tier0["input"])
	}
}

// canonical 键序：provider 形状按 fieldOrders，未知键排尾。
func TestCanonicalKeyOrder(t *testing.T) {
	out, err := MarshalCanonical(map[string]any{
		"models":    []any{},
		"id":        "x",
		"checkedAt": "2026-11-24",
		"name":      "X",
		"zzExtra":   1.0,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	pos := func(k string) int { return strings.Index(s, `"`+k+`"`) }
	if !(pos("id") < pos("name") && pos("name") < pos("checkedAt") &&
		pos("checkedAt") < pos("models") && pos("models") < pos("zzExtra")) {
		t.Errorf("键序不符 canonical 序:\n%s", s)
	}
}

// 字符串转义对齐 Python json.dumps(ensure_ascii=False)：
// UTF-8 原文输出，仅转义引号/反斜杠/控制字符。
func TestWriteJSONStringUTF8(t *testing.T) {
	var b bytes.Buffer
	writeJSONString(&b, "中文 \"引号\" 混合\tend")
	got := b.String()
	if !strings.Contains(got, "中文") || !strings.Contains(got, `\"引号\"`) ||
		!strings.Contains(got, `\t`) {
		t.Errorf("转义不符: %s", got)
	}
	if strings.Contains(got, `\u`) {
		t.Errorf("不应出现 \\u 转义: %s", got)
	}
}
