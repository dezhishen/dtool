package action

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dezhishen/dtool/internal/workspace"
)

func newTestRecorder(t *testing.T) (*Recorder, *workspace.Workspace) {
	t.Helper()
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Recorder{WS: ws}, ws
}

func makeQueryResult(t *testing.T, rec *Recorder, ws *workspace.Workspace, body string, rows int) *Action {
	t.Helper()
	a, err := rec.Start(StartParams{Type: "query", Input: map[string]any{"sql": "SELECT 1"}, Command: "dtool test"})
	if err != nil {
		t.Fatal(err)
	}
	dir, err := ws.ActionOutputDir("query", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "result.json")
	if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rec.Success(a, &Output{Files: []string{ws.Rel(f)}, RowCount: rows,
		Columns: []string{"n"}, Summary: "查询返回 1 行"}); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestCollectResultsAndStats(t *testing.T) {
	rec, ws := newTestRecorder(t)
	makeQueryResult(t, rec, ws, `[{"n":1}]`, 1)
	makeQueryResult(t, rec, ws, `[{"n":1},{"n":2}]`, 2)

	sets, err := rec.CollectResults(ResultQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 2 {
		t.Fatalf("sets = %d", len(sets))
	}
	if sets[0].RowCount != 2 || sets[0].Bytes == 0 || !sets[0].Files[0].Exists || sets[0].Files[0].External {
		t.Fatalf("newest set = %+v", sets[0])
	}
	st := ResultStatistics(sets)
	if st.Sets != 2 || st.Files != 2 || st.Rows != 3 || st.Bytes == 0 || st.Missing != 0 || st.ByType["query"].Sets != 2 {
		t.Fatalf("stats = %+v", st)
	}

	hashed, err := rec.CollectResults(ResultQuery{Hash: true})
	if err != nil {
		t.Fatal(err)
	}
	if hashed[0].Files[0].SHA256 == "" {
		t.Fatalf("hash missing: %+v", hashed[0].Files[0])
	}
	if rep := VerifyResults(hashed); !rep.OK || rep.Files != 2 {
		t.Fatalf("verify = %+v", rep)
	}
}

func TestGCKeepsLatestAndProtectsReferences(t *testing.T) {
	rec, ws := newTestRecorder(t)
	old := makeQueryResult(t, rec, ws, `[{"n":1}]`, 1)
	latest := makeQueryResult(t, rec, ws, `[{"n":2}]`, 1)

	rep, err := rec.GCResults(GCOptions{OlderThan: 0, KeepLast: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.DryRun || len(rep.WouldDelete) != 1 || rep.WouldDelete[0].ActionID != old.ID {
		t.Fatalf("dry-run = %+v", rep)
	}
	if _, err := os.Stat(ws.Abs(old.MainFile())); err != nil {
		t.Fatal("dry-run must not delete")
	}

	// 下游 Action 引用旧结果：默认跳过
	if _, err := rec.Start(StartParams{Type: "query", Input: map[string]any{"source": "action:" + old.ID}}); err != nil {
		t.Fatal(err)
	}
	rep, err = rec.GCResults(GCOptions{OlderThan: 0, KeepLast: 1, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Deleted) != 0 || len(rep.Skipped) == 0 {
		t.Fatalf("referenced result should be kept: %+v", rep)
	}

	// --force 才允许删除被引用结果；latest 仍由 keep-last 保护
	rep, err = rec.GCResults(GCOptions{OlderThan: 0, KeepLast: 1, Apply: true, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Deleted) != 1 || rep.Deleted[0].ActionID != old.ID {
		t.Fatalf("force = %+v", rep)
	}
	if _, err := os.Stat(ws.Abs(latest.MainFile())); err != nil {
		t.Fatal("keep-last result was deleted")
	}
}

func TestGCLeavesExternalFiles(t *testing.T) {
	rec, _ := newTestRecorder(t)
	a, err := rec.Start(StartParams{Type: "query", Input: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "out.csv")
	if err := os.WriteFile(external, []byte("a,b\n1,2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rec.Success(a, &Output{Files: []string{external}, Summary: "查询返回 1 行"}); err != nil {
		t.Fatal(err)
	}
	sets, err := rec.CollectResults(ResultQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if !sets[0].Files[0].External {
		t.Fatalf("external not marked: %+v", sets[0].Files[0])
	}
	rep, err := rec.GCResults(GCOptions{OlderThan: 0, KeepLast: 0, Apply: true, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Deleted) != 0 {
		t.Fatalf("external file deleted: %+v", rep)
	}
	if _, err := os.Stat(external); err != nil {
		t.Fatal("external file removed")
	}
}
