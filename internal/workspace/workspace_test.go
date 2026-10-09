package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenCreatesLayoutAndKeepsID(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".dtool")
	w, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{w.ActionsDir(), w.OutputsDir()} {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			t.Fatalf("missing %s", d)
		}
	}
	w2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if w.Info.WorkspaceID == "" || w.Info.WorkspaceID != w2.Info.WorkspaceID {
		t.Fatalf("id changed: %q vs %q", w.Info.WorkspaceID, w2.Info.WorkspaceID)
	}
}

func TestRelAbs(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".dtool")
	w, _ := Open(root)
	p := filepath.Join(root, "outputs", "x", "data.json")
	if got := w.Rel(p); got != ".dtool/outputs/x/data.json" {
		t.Fatalf("Rel = %s", got)
	}
	if got := w.Abs(".dtool/outputs/x/data.json"); got != p {
		t.Fatalf("Abs = %s", got)
	}
	if got := w.Abs(p); got != p {
		t.Fatalf("Abs(abs) = %s", got)
	}
}

func TestLockExcludesAndReleases(t *testing.T) {
	w, _ := Open(filepath.Join(t.TempDir(), ".dtool"))
	unlock, err := w.Lock()
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan func(), 1)
	go func() {
		u, err := w.Lock()
		if err != nil {
			t.Error(err)
		}
		got <- u
	}()
	select {
	case <-got:
		t.Fatal("second lock acquired while held")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	select {
	case u := <-got:
		u()
	case <-time.After(2 * time.Second):
		t.Fatal("second lock never acquired")
	}
}

func TestStaleLockIsBroken(t *testing.T) {
	w, _ := Open(filepath.Join(t.TempDir(), ".dtool"))
	lock := filepath.Join(w.Root, ".lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	os.Chtimes(lock, old, old)
	unlock, err := w.Lock()
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

func TestWriteJSONAtomic(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.json")
	if err := WriteJSONAtomic(p, map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	var v map[string]int
	if err := ReadJSON(p, &v); err != nil || v["a"] != 1 {
		t.Fatalf("v=%v err=%v", v, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
	if err := WriteJSONAtomic(p, func() {}); err == nil {
		t.Fatal("unmarshalable value accepted")
	}
	if err := ReadJSON(p, &v); err != nil || v["a"] != 1 {
		t.Fatal("failed write corrupted the file")
	}
}

func TestActionOutputDirGroupsByType(t *testing.T) {
	w, _ := Open(filepath.Join(t.TempDir(), ".dtool"))
	cases := map[string]string{
		"query":     filepath.Join("queries", "ID1"),
		"visualize": filepath.Join("charts", "ID1"),
		"pipeline":  filepath.Join("pipeline", "ID1"),
	}
	for typ, want := range cases {
		d, err := w.ActionOutputDir(typ, "ID1")
		if err != nil {
			t.Fatal(err)
		}
		if d != filepath.Join(w.OutputsDir(), want) {
			t.Errorf("%s: %s", typ, d)
		}
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			t.Errorf("%s: dir not created", typ)
		}
	}
}

func TestDatasetsDirIsIndependentOfOutputs(t *testing.T) {
	w, _ := Open(filepath.Join(t.TempDir(), ".dtool"))
	if w.DatasetsDir() != filepath.Join(w.Root, "datasets") {
		t.Fatalf("DatasetsDir = %s", w.DatasetsDir())
	}
	if st, err := os.Stat(w.DatasetsDir()); err != nil || !st.IsDir() {
		t.Fatal("datasets dir not created")
	}
}

func TestSafeName(t *testing.T) {
	cases := map[string]string{
		"sales":         "sales",
		"销量 2026/Q4":    "销量_2026_Q4",
		"../../etc":     "_.._etc",
		"..":            "dataset",
		"":              "dataset",
		"  a b  ":       "a_b",
		"a\\b:c*d":      "a_b_c_d",
		".hidden":       "hidden",
		"keep-this_1.2": "keep-this_1.2",
	}
	for in, want := range cases {
		if got := SafeName(in); got != want {
			t.Errorf("SafeName(%q) = %q, want %q", in, got, want)
		}
	}
	if n := len([]rune(SafeName(strings.Repeat("长", 200)))); n != 64 {
		t.Errorf("length = %d", n)
	}
}
