package action

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dezhishen/dtool/internal/dataset"
	"github.com/dezhishen/dtool/internal/workspace"
	"github.com/dezhishen/dtool/pkg/types"
)

func newRec(t *testing.T) *Recorder {
	t.Helper()
	ws, err := workspace.Open(filepath.Join(t.TempDir(), ".dtool"))
	if err != nil {
		t.Fatal(err)
	}
	return &Recorder{WS: ws}
}

func mustStart(t *testing.T, r *Recorder, typ string, p StartParams) *Action {
	t.Helper()
	p.Type = typ
	a, err := r.Start(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestLifecycleSuccessAndFail(t *testing.T) {
	r := newRec(t)
	a := mustStart(t, r, "query", StartParams{Command: "dtool query", Metadata: Metadata{Tags: []string{"t"}}})
	if got, _ := r.Get(a.ID); got.Status != StatusRunning || got.Pid == 0 {
		t.Fatalf("after start: %+v", got)
	}
	if err := r.Success(a, &Output{Files: []string{"x.json"}, Summary: "ok"}); err != nil {
		t.Fatal(err)
	}
	got, _ := r.Get(a.ID)
	if got.Status != StatusSuccess || got.FinishedAt == nil || got.Output.Summary != "ok" || got.Metadata.Tags[0] != "t" {
		t.Fatalf("after success: %+v", got)
	}

	b := mustStart(t, r, "convert", StartParams{})
	if err := r.Fail(b, types.Errorf(types.CodeNotFound, "nope").WithDetail("d")); err != nil {
		t.Fatal(err)
	}
	gb, _ := r.Get(b.ID)
	if gb.Status != StatusFailed || gb.Error.Code != types.CodeNotFound || gb.Error.Detail != "d" {
		t.Fatalf("after fail: %+v", gb)
	}

	entries, total, _ := r.List(Filter{})
	if total != 2 || entries[0].ID != b.ID {
		t.Fatalf("list not newest-first: %+v", entries)
	}
	if entries[0].Summary != "nope" || entries[1].Summary != "ok" {
		t.Fatalf("summaries: %+v", entries)
	}
}

func TestListFilters(t *testing.T) {
	r := newRec(t)
	for i, typ := range []string{"convert", "query", "query"} {
		a := mustStart(t, r, typ, StartParams{})
		if i == 2 {
			r.Fail(a, errors.New("x"))
		} else {
			r.Success(a, &Output{Summary: "s"})
		}
	}
	if e, total, _ := r.List(Filter{Type: "query"}); total != 2 || len(e) != 2 {
		t.Fatalf("type filter: %d", total)
	}
	if e, _, _ := r.List(Filter{Type: "query", Status: StatusSuccess}); len(e) != 1 {
		t.Fatalf("type+status: %d", len(e))
	}
	if e, total, _ := r.List(Filter{Limit: 1}); len(e) != 1 || total != 3 {
		t.Fatalf("limit: len=%d total=%d", len(e), total)
	}
}

func TestStaleDetection(t *testing.T) {
	r := newRec(t)
	a := mustStart(t, r, "query", StartParams{})
	a.Pid = 2147480000 // 不存在的进程
	if err := r.save(a); err != nil {
		t.Fatal(err)
	}
	got, _ := r.Get(a.ID)
	if got.Status != StatusStale {
		t.Fatalf("status = %s", got.Status)
	}
	if e, _, _ := r.List(Filter{Status: StatusStale}); len(e) != 1 {
		t.Fatal("stale not listed")
	}
	if _, err := r.Resolve("action:" + a.ID); err == nil {
		t.Fatal("stale action resolved as data source")
	}
}

func TestAnnotatePersists(t *testing.T) {
	r := newRec(t)
	a := mustStart(t, r, "query", StartParams{})
	r.Success(a, &Output{Summary: "s"})
	if _, err := r.Annotate(a.ID, "含税", "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Annotate(a.ID, "再补充", "ai-agent"); err != nil {
		t.Fatal(err)
	}
	got, _ := r.Get(a.ID)
	if len(got.Annotations) != 2 || got.Annotations[0].Text != "含税" || got.Annotations[1].By != "ai-agent" {
		t.Fatalf("annotations: %+v", got.Annotations)
	}
	if got.Status != StatusSuccess {
		t.Fatalf("annotate changed status: %s", got.Status)
	}
}

func TestIDValidationAndNotFound(t *testing.T) {
	r := newRec(t)
	mustStart(t, r, "query", StartParams{})
	for _, id := range []string{"", "../x", "01HX", "../../etc/passwd", "zzzzzzzzzzzzzzzzzzzzzzzzzz"} {
		_, err := r.Get(id)
		var te *types.Error
		if !errors.As(err, &te) || te.Code != types.CodeNotFound {
			t.Errorf("Get(%q) err = %v", id, err)
		}
	}
	_, err := r.Get(NewID())
	var te *types.Error
	if !errors.As(err, &te) || te.Detail == "" {
		t.Fatalf("not-found error should list recent actions: %v", err)
	}
}

func TestIndexRebuildAndReindex(t *testing.T) {
	r := newRec(t)
	a := mustStart(t, r, "convert", StartParams{})
	r.Success(a, &Output{Files: []string{"d.json"}, Summary: "s"})
	b := mustStart(t, r, "query", StartParams{DerivedFrom: a.ID})
	r.Success(b, &Output{Summary: "q"})

	os.Remove(r.WS.IndexPath())
	if e, total, _ := r.List(Filter{}); total != 2 || len(e) != 2 {
		t.Fatalf("missing index not rebuilt: %d", total)
	}
	os.WriteFile(r.WS.IndexPath(), []byte("{broken"), 0o644)
	if e, _, _ := r.List(Filter{}); len(e) != 2 {
		t.Fatalf("corrupt index not rebuilt: %d", len(e))
	}
	n, err := r.Reindex()
	if err != nil || n != 2 {
		t.Fatalf("Reindex = %d, %v", n, err)
	}
	var idx Index
	if err := workspace.ReadJSON(r.WS.IndexPath(), &idx); err != nil || len(idx.Actions) != 2 {
		t.Fatalf("index not rewritten: %v %+v", err, idx)
	}
}

func TestTrace(t *testing.T) {
	r := newRec(t)
	a := mustStart(t, r, "convert", StartParams{})
	r.Success(a, &Output{Summary: "a"})
	b := mustStart(t, r, "query", StartParams{DerivedFrom: a.ID})
	r.Success(b, &Output{Summary: "b"})
	c := mustStart(t, r, "visualize", StartParams{DerivedFrom: b.ID})
	r.Success(c, &Output{Summary: "c"})
	d := mustStart(t, r, "query", StartParams{ParentID: a.ID})
	r.Success(d, &Output{Summary: "d"})
	other := mustStart(t, r, "query", StartParams{})
	r.Success(other, &Output{Summary: "o"})

	up, down, err := r.Trace(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(up) != 1 || up[0].ID != a.ID {
		t.Fatalf("upstream: %+v", up)
	}
	if len(down) != 1 || down[0].ID != c.ID {
		t.Fatalf("downstream: %+v", down)
	}
	_, down, _ = r.Trace(a.ID)
	if len(down) != 3 {
		t.Fatalf("downstream of root: %+v", down)
	}
	if _, _, err := r.Trace(NewID()); err == nil {
		t.Fatal("trace of unknown id succeeded")
	}
}

func TestResolve(t *testing.T) {
	r := newRec(t)
	dir, _ := r.WS.ActionOutputDir("query", "x")
	data := filepath.Join(dir, "d.json")
	os.WriteFile(data, []byte("[]"), 0o644)

	conv := mustStart(t, r, "convert", StartParams{})
	r.Success(conv, &Output{Files: []string{r.WS.Rel(data)}, Summary: "c"})
	failed := mustStart(t, r, "query", StartParams{})
	r.Fail(failed, errors.New("x"))
	noData := mustStart(t, r, "visualize", StartParams{})
	r.Success(noData, &Output{Summary: "v"})

	res, err := r.Resolve("action:" + conv.ID)
	if err != nil || res.ActionID != conv.ID || res.Path != data {
		t.Fatalf("action ref: %+v %v", res, err)
	}
	res, err = r.Resolve("latest:convert")
	if err != nil || res.ActionID != conv.ID {
		t.Fatalf("latest ref: %+v %v", res, err)
	}
	if res, err = r.Resolve(data); err != nil || res.ActionID != "" || res.Path != data {
		t.Fatalf("file ref: %+v %v", res, err)
	}
	for _, ref := range []string{"action:" + failed.ID, "action:" + noData.ID, "latest:query", "latest:nothing", "/no/such/file.json"} {
		_, err := r.Resolve(ref)
		var te *types.Error
		if !errors.As(err, &te) || te.Code != types.CodeNotFound {
			t.Errorf("Resolve(%q) err = %v", ref, err)
		}
	}
}

func TestPreview(t *testing.T) {
	rows := make([]types.Row, 30)
	if p, trunc := Preview(rows, 0); len(p) != 20 || !trunc {
		t.Fatalf("default: %d %v", len(p), trunc)
	}
	if p, trunc := Preview(rows, 50); len(p) != 30 || trunc {
		t.Fatalf("no truncation: %d %v", len(p), trunc)
	}
}

func TestResolveDatasetRef(t *testing.T) {
	r := newRec(t)
	ds := &dataset.Store{WS: r.WS}
	stage, _ := ds.NewStaging()
	os.WriteFile(filepath.Join(stage, dataset.DataFile), []byte("[]"), 0o644)
	os.WriteFile(filepath.Join(stage, dataset.SchemaFile), []byte("{}"), 0o644)
	if _, err := ds.Commit(stage, dataset.Meta{Name: "sales", ActionID: "01ARZ3NDEKTSV4RRFFQ69G5FAV"}); err != nil {
		t.Fatal(err)
	}

	res, err := r.Resolve("dataset:sales")
	if err != nil || res.Path != ds.Path("sales") || res.ActionID != "01ARZ3NDEKTSV4RRFFQ69G5FAV" {
		t.Fatalf("resolve: %+v %v", res, err)
	}
	var te *types.Error
	if _, err := r.Resolve("dataset:nope"); !errors.As(err, &te) || te.Code != types.CodeNotFound || te.Hint == "" {
		t.Fatalf("unknown dataset: %v", err)
	}
}
