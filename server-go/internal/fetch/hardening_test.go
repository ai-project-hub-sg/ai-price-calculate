package fetch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	if err := writeFileAtomic(path, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second" {
		t.Fatalf("file content = %q", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "models.json" {
		t.Fatalf("temporary files were not cleaned up: %+v", entries)
	}
}

func TestLoadDatasetRejectsInvalidProviders(t *testing.T) {
	for _, content := range []string{`{}`, `{"providers":null}`, `{"providers":{}}`} {
		path := filepath.Join(t.TempDir(), "models.json")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadDataset(path); err == nil {
			t.Errorf("LoadDataset accepted %s", content)
		}
	}
}
