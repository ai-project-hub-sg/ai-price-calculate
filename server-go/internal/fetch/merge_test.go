package fetch

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// decodeFixtureRows 按真实解析产物的类型解码 fixture 行：upToTokens 等
// 整数词法必须是 int64（真实解析里 tokens() 返回 int），价格保持 float64。
// 直接 json.Unmarshal 会把整数解成 float64，写回时 128000 变 128000.0。
func decodeFixtureRows(t *testing.T, path string) []Row {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var rows []Row
	if err := dec.Decode(&rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		normalizeNumbers(row)
	}
	return rows
}

// 合并一致性：Go MergeProvider 在 merge_input.json 上的结果必须与
// Python merge_provider 的基准（merge_expected.json，gen_fixtures.py 生成）一致。
// 覆盖 updated/added/missingInSource 三分支；未动 provider 需原字节直通。
func TestMergeParityWithPython(t *testing.T) {
	ds, err := LoadDataset(filepath.Join("testdata", "merge_input.json"))
	if err != nil {
		t.Fatal(err)
	}
	rows := decodeFixtureRows(t, filepath.Join("testdata", "ds.expected.json"))
	res := ds.MergeProvider("deepseek", rows, "2026-11-24")

	expRaw, err := os.ReadFile(filepath.Join("testdata", "merge_expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	var exp struct {
		Dataset map[string]any `json:"dataset"`
		Report  map[string]any `json:"report"`
	}
	if err := json.Unmarshal(expRaw, &exp); err != nil {
		t.Fatal(err)
	}

	// 合并报告逐字段一致
	norm := func(v any) any {
		b, _ := json.Marshal(v)
		var out any
		json.Unmarshal(b, &out)
		return out
	}
	if !reflect.DeepEqual(norm(res), exp.Report) {
		gr, _ := json.Marshal(norm(res))
		er, _ := json.Marshal(exp.Report)
		t.Errorf("合并报告不符:\n got=%s\nwant=%s", gr, er)
	}

	// 合并后的 deepseek provider 深度一致（键序无关）
	var gotProv, wantProv map[string]any
	for _, p := range exp.Dataset["providers"].([]any) {
		pm := p.(map[string]any)
		if pm["id"] == "deepseek" {
			wantProv = pm
		}
	}
	out, err := ds.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	for _, p := range got["providers"].([]any) {
		pm := p.(map[string]any)
		if pm["id"] == "deepseek" {
			gotProv = pm
		}
	}
	if !reflect.DeepEqual(gotProv, wantProv) {
		g, _ := json.MarshalIndent(gotProv, "", "  ")
		w, _ := json.MarshalIndent(wantProv, "", "  ")
		t.Errorf("deepseek 合并结果不符:\n got=%s\nwant=%s", g, w)
	}

	// 未动的 xai 子树原字节直通（零 diff 保证）
	if !bytes.Contains(out, ds.rawProv["xai"]) {
		t.Error("未合并的 xai provider 未原字节直通")
	}
}

// 字节级写回对拍（数字格式/键序/缩进的严格守门）：Go merge 后写回输出
// 必须逐字节等于 Python 参考实现的写回字节（merge_expected_file.json，
// gen_fixtures.py 生成）。语义比较看不出 64000 vs 64000.0——正是这类
// 分歧曾穿过语义测试进入 CR-003。fixture 由 testdata/.gitattributes
// 固定 LF 检出，跨平台确定性。
func TestMergeWritebackByteParityWithPython(t *testing.T) {
	ds, err := LoadDataset(filepath.Join("testdata", "merge_input.json"))
	if err != nil {
		t.Fatal(err)
	}
	rows := decodeFixtureRows(t, filepath.Join("testdata", "ds.expected.json"))
	ds.MergeProvider("deepseek", rows, "2026-11-24")
	out, err := ds.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "merge_expected_file.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, want) {
		// 定位首个分歧行，便于一眼看到是数字/键序/缩进哪类问题
		gl, wl := bytes.Split(out, []byte("\n")), bytes.Split(want, []byte("\n"))
		for i := 0; i < len(gl) && i < len(wl); i++ {
			if !bytes.Equal(gl[i], wl[i]) {
				t.Fatalf("写回字节分歧（行 %d）:\n got=%q\nwant=%q", i+1, gl[i], wl[i])
			}
		}
		t.Fatalf("写回字节长度分歧: got %d 行, want %d 行", len(gl), len(wl))
	}
}

// 未做任何 merge 时 Marshal 输出与输入文件逐字节一致（零 diff 往返）。
func TestMarshalUntouchedByteIdentical(t *testing.T) {
	path := filepath.Join("testdata", "merge_input.json")
	in, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ds, err := LoadDataset(path)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ds.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimRight(in, "\r\n"), bytes.TrimRight(out, "\r\n")) {
		t.Error("未 merge 时写回输出与输入不逐字节一致")
	}
}
