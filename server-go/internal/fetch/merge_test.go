package fetch

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// 合并一致性：Go MergeProvider 在 merge_input.json 上的结果必须与
// Python merge_provider 的基准（merge_expected.json，gen_fixtures.py 生成）一致。
// 覆盖 updated/added/missingInSource 三分支；未动 provider 需原字节直通。
func TestMergeParityWithPython(t *testing.T) {
	ds, err := LoadDataset(filepath.Join("testdata", "merge_input.json"))
	if err != nil {
		t.Fatal(err)
	}
	rowsRaw, err := os.ReadFile(filepath.Join("testdata", "ds.expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []Row
	if err := json.Unmarshal(rowsRaw, &rows); err != nil {
		t.Fatal(err)
	}
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
