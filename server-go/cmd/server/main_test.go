package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultDataDirUsesExecutable(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	got, err := defaultDataDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Dir(executable); got != want {
		t.Fatalf("defaultDataDir() = %q, want %q", got, want)
	}
}
