package fetch

import (
	"bytes"
	"strings"
	"testing"
)

// canonical marshal 的数字格式：大整数不许科学计数法（曾出现 1050000→1.05e+06），
// 对齐 Python json.dump 的十进制输出。
func TestMarshalCanonicalNumbers(t *testing.T) {
	out, err := MarshalCanonical(map[string]any{
		"contextWindow": float64(1050000),
		"input":         float64(0.006),
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "e+") || strings.Contains(s, "E+") {
		t.Errorf("出现科学计数法: %s", s)
	}
	if !strings.Contains(s, "1050000") || !strings.Contains(s, "0.006") {
		t.Errorf("数字格式不符: %s", s)
	}
}

// canonical 键序：provider 形状按 fieldOrders，未知键排尾。
func TestCanonicalKeyOrder(t *testing.T) {
	out, err := MarshalCanonical(map[string]any{
		"models":   []any{},
		"id":       "x",
		"checkedAt": "2026-11-24",
		"name":     "X",
		"zzExtra":  1.0,
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
