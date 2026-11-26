package fetch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestXAIUnknownContextIsNull(t *testing.T) {
	rows := ParseXAI("| grok-test (< 200k prompt tokens) | unknown | $1 | $0.1 | $2 |")
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if value, exists := rows[0]["contextWindow"]; !exists || value != nil {
		t.Fatalf("contextWindow = %v (exists=%v), want explicit null", value, exists)
	}
}

// 对照测试：Go 解析器输出必须与 Python fetcher.py 对同一 fixture 的
// 解析结果（*.expected.json，由 Python 生成）完全一致。
func TestParsersMatchPython(t *testing.T) {
	cases := []struct {
		fixture string
		parse   func(string) []Row
	}{
		{"xai", ParseXAI},
		{"ds", ParseDeepSeek},
		{"zai", ParseZAI},
		{"bm", ParseBigmodel},
		{"openai", ParseOpenAI},
		{"anthropic", ParseAnthropic},
		{"google", ParseGoogle},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			md, err := os.ReadFile(filepath.Join("testdata", c.fixture+".md"))
			if err != nil {
				t.Fatal(err)
			}
			expRaw, err := os.ReadFile(filepath.Join("testdata", c.fixture+".expected.json"))
			if err != nil {
				t.Fatal(err)
			}
			var want []any
			if err := json.Unmarshal(expRaw, &want); err != nil {
				t.Fatal(err)
			}
			// JSON 归一化两侧（统一数字为 float64、键序无关）
			gotRaw, err := json.Marshal(c.parse(string(md)))
			if err != nil {
				t.Fatalf("marshal got: %v", err)
			}
			var got []any
			if err := json.Unmarshal(gotRaw, &got); err != nil {
				t.Fatal(err)
			}
			if len(got) != len(want) {
				t.Fatalf("行数不符 got %d want %d", len(got), len(want))
			}
			if !reflect.DeepEqual(got, want) {
				for i := range want {
					if !reflect.DeepEqual(got[i], want[i]) {
						g, _ := json.Marshal(got[i])
						w, _ := json.Marshal(want[i])
						t.Errorf("第 %d 行不符:\n got=%s\nwant=%s", i, g, w)
					}
				}
			}
		})
	}
}
