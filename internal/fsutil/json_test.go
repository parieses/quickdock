package fsutil

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

type sample struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

func TestReadJSON(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.json")
	if err := os.WriteFile(p, []byte(`{"name":"a","n":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadJSON[sample](p)
	if err != nil || got.Name != "a" || got.N != 2 {
		t.Fatalf("got %+v, err %v", got, err)
	}
}

func TestReadJSONErrors(t *testing.T) {
	if _, err := ReadJSON[sample](filepath.Join(t.TempDir(), "missing.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("want fs.ErrNotExist, got %v", err)
	}
	p := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(p, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadJSON[sample](p); err == nil {
		t.Fatal("want parse error")
	}
}

func TestTryReadJSON(t *testing.T) {
	if got, ok := TryReadJSON[sample](filepath.Join(t.TempDir(), "missing.json")); ok || got != (sample{}) {
		t.Fatalf("missing: got %+v ok=%v", got, ok)
	}
	p := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(p, []byte(`not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := TryReadJSON[sample](p); ok || got != (sample{}) {
		t.Fatalf("bad: got %+v ok=%v", got, ok)
	}
	if err := os.WriteFile(p, []byte(`{"name":"ok"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := TryReadJSON[sample](p); !ok || got.Name != "ok" {
		t.Fatalf("valid: got %+v ok=%v", got, ok)
	}
}

func TestIsNotExist(t *testing.T) {
	_, err := ReadJSON[sample](filepath.Join(t.TempDir(), "none.json"))
	if !IsNotExist(err) {
		t.Fatalf("want IsNotExist, got %v", err)
	}
}
