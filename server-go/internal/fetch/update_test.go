package fetch

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// parsesEmpty 谓词：真实价目页不可误判为空（否则被错误送去代理）；
// 软墙/地区限制页必须判空；解析 panic 不判空（留给 merge 报 parse-error）。
func TestMergeMissingUsesDatasetOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	data := `{"providers":[{"id":"x","models":[{"id":"z"},{"id":"a"},{"id":"m"}]}]}`
	if err := os.WriteFile(path, []byte(data), 0o640); err != nil {
		t.Fatal(err)
	}
	ds, err := LoadDataset(path)
	if err != nil {
		t.Fatal(err)
	}
	res := ds.MergeProvider("x", []Row{{"id": "a", "tiers": []Row{}}}, "2026-11-25")
	got := res["missingInSource"].([]string)
	if strings.Join(got, ",") != "z,m" {
		t.Fatalf("missingInSource = %v, want dataset order z,m", got)
	}
}

func TestAtomicWritePreservesPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not enforce POSIX permission bits")
	}
	path := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(path, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, []byte("new"), filePermission(path, 0o644)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want 640", info.Mode().Perm())
	}
}

func TestParsesEmptyPredicate(t *testing.T) {
	parsers := map[string]func(string) []Row{
		"xai": ParseXAI, "ds": ParseDeepSeek, "zai": ParseZAI,
		"bm": ParseBigmodel, "openai": ParseOpenAI,
		"anthropic": ParseAnthropic, "google": ParseGoogle,
	}
	for fixture, fn := range parsers {
		md, err := os.ReadFile(filepath.Join("testdata", fixture+".md"))
		if err != nil {
			t.Fatal(err)
		}
		if parsesEmpty(fn, string(md)) {
			t.Errorf("%s 真实价目页被误判为解析空", fixture)
		}
	}
	// >500B 但无任何价目表的软墙/地区限制页 → 全部解析器判空
	softBlock := strings.Repeat("<html><body><h1>Not available in your region</h1></body></html>\n", 10)
	for name, fn := range parsers {
		if !parsesEmpty(fn, softBlock) {
			t.Errorf("%s 软墙页未被判空", name)
		}
	}
	panicky := func(string) []Row { panic("boom") }
	if parsesEmpty(panicky, "x") {
		t.Error("解析 panic 不应被判为解析空")
	}
}

// 胜出校验字节门槛（E1 回归）：小 body 即使 200/可解析也不得胜出——
// 否则厂商被移出待决清单后又被最终门槛丢弃，既不合并也不在 unreachable。
func TestValidWinnerBodySizeGate(t *testing.T) {
	md, err := os.ReadFile(filepath.Join("testdata", "ds.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(md) <= minPricingBytes {
		t.Fatalf("ds.md fixture 应大于门槛：%d", len(md))
	}
	small := make([]byte, minPricingBytes-100)
	for i := range small {
		small[i] = 'x'
	}
	cases := []struct {
		name string
		pid  string
		body []byte
		want bool
	}{
		{"无解析器源小 body（moonshot 地区限制页场景）", "moonshot", small, false},
		{"无解析器源大 body", "moonshot", append(small, make([]byte, 200)...), true},
		{"有解析器源小 body（字节门槛先于解析）", "deepseek", small, false},
		{"有解析器源真实价目页", "deepseek", md, true},
		{"有解析器源大而不可解析", "deepseek", append(small, make([]byte, 2000)...), false},
	}
	for _, c := range cases {
		if got := validWinnerBody(c.pid, c.body); got != c.want {
			t.Errorf("%s: validWinnerBody = %v, want %v", c.name, got, c.want)
		}
	}
}
