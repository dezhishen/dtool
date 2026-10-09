package dataset

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dezhishen/dtool/internal/workspace"
	"github.com/dezhishen/dtool/pkg/types"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	ws, err := workspace.Open(filepath.Join(t.TempDir(), ".dtool"))
	if err != nil {
		t.Fatal(err)
	}
	return &Store{WS: ws}
}

func stage(t *testing.T, s *Store, data string) string {
	t.Helper()
	d, err := s.NewStaging()
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(d, DataFile), []byte(data), 0o644)
	os.WriteFile(filepath.Join(d, SchemaFile), []byte("{}"), 0o644)
	return d
}

func TestCommitOverwriteRefreshesUpdatedAt(t *testing.T) {
	s := newStore(t)
	m1, err := s.Commit(stage(t, s, `[1]`), Meta{Name: "销量 表", ActionID: "A1", RecordCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	if m1.Name != "销量_表" || !m1.UpdatedAt.Equal(m1.CreatedAt) {
		t.Fatalf("m1 = %+v", m1)
	}
	if s.Dir("销量 表") != filepath.Join(s.WS.DatasetsDir(), "销量_表") {
		t.Fatal("dir not under datasets/")
	}

	later := m1.UpdatedAt.Add(time.Hour)
	m2, err := s.Commit(stage(t, s, `[1,2]`), Meta{Name: "销量_表", ActionID: "A2", RecordCount: 2, UpdatedAt: later})
	if err != nil {
		t.Fatal(err)
	}
	if !m2.CreatedAt.Equal(m1.CreatedAt) || !m2.UpdatedAt.Equal(later) || m2.ActionID != "A2" {
		t.Fatalf("m2 = %+v", m2)
	}
	if b, _ := os.ReadFile(s.Path("销量_表")); string(b) != "[1,2]" {
		t.Fatalf("data = %s", b)
	}
	info, _ := s.Info("销量_表")
	if info.SchemaFile != ".dtool/datasets/销量_表/data.schema.json" || info.DataFile != ".dtool/datasets/销量_表/data.json" {
		t.Fatalf("info = %+v", info)
	}
	if _, err := os.Stat(s.SchemaPath("销量_表")); err != nil {
		t.Fatal("schema missing")
	}
}

func TestCommitRequiresSchema(t *testing.T) {
	s := newStore(t)
	d, _ := s.NewStaging()
	os.WriteFile(filepath.Join(d, DataFile), []byte(`[]`), 0o644)
	if _, err := s.Commit(d, Meta{Name: "x"}); err == nil {
		t.Fatal("commit without schema accepted")
	}
	if _, err := s.Get("x"); err == nil {
		t.Fatal("partial dataset was created")
	}
}

func TestCommitRequiresData(t *testing.T) {
	s := newStore(t)
	empty, _ := s.NewStaging()
	if _, err := s.Commit(empty, Meta{Name: "x"}); err == nil {
		t.Fatal("commit without data accepted")
	}
}

func TestListGetLookupDelete(t *testing.T) {
	s := newStore(t)
	for _, n := range []string{"b", "a"} {
		if _, err := s.Commit(stage(t, s, `[]`), Meta{Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	stage(t, s, `[]`) // 遗留的暂存目录不应被当作数据集

	items, _ := s.List()
	if len(items) != 2 || items[0].Name != "a" || items[1].Name != "b" || items[0].SchemaFile == "" || items[1].SchemaFile == "" {
		t.Fatalf("list = %+v", items)
	}
	if p, ok := s.Lookup("a"); !ok || p != s.Path("a") {
		t.Fatalf("lookup = %s %v", p, ok)
	}
	if _, ok := s.Lookup("zzz"); ok {
		t.Fatal("lookup of missing dataset succeeded")
	}

	if err := s.Delete("a"); err != nil {
		t.Fatal(err)
	}
	var te *types.Error
	if _, err := s.Get("a"); !errors.As(err, &te) || te.Code != types.CodeNotFound {
		t.Fatalf("get after delete: %v", err)
	}
	if err := s.Delete("a"); !errors.As(err, &te) {
		t.Fatalf("double delete: %v", err)
	}
}

func TestNamesCannotEscapeDatasetsDir(t *testing.T) {
	s := newStore(t)
	if _, err := s.Commit(stage(t, s, `[]`), Meta{Name: "../../evil"}); err != nil {
		t.Fatal(err)
	}
	items, _ := s.List()
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	if _, err := os.Stat(filepath.Join(s.WS.Root, "..", "evil")); err == nil {
		t.Fatal("escaped datasets dir")
	}
}
