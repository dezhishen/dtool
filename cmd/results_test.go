package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestActionsResultsListStatVerifyGC(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeJSON(t, filepath.Join(dir, "d.json"), `[{"region":"A","n":1},{"region":"B","n":2}]`)

	if _, err := run(t, "query", "--sql", `SELECT region, n FROM "d.json"`); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "query", "--format", "csv", "--output", "out.csv", "--sql", `SELECT region, n FROM "d.json"`); err != nil {
		t.Fatal(err)
	}

	list, err := run(t, "actions", "results", "list")
	if err != nil {
		t.Fatal(err)
	}
	if list["total"].(float64) != 2 {
		t.Fatalf("list: %v", list)
	}
	first := list["results"].([]any)[0].(map[string]any)
	if _, ok := first["rows"]; ok {
		t.Fatalf("results list 不应含 rows: %v", first)
	}
	if len(first["files"].([]any)) != 2 {
		t.Fatalf("query with --output should have 2 files: %v", first)
	}

	stat, err := run(t, "actions", "results", "stat")
	if err != nil {
		t.Fatal(err)
	}
	if stat["sets"].(float64) != 2 || stat["rows"].(float64) != 4 || stat["by_type"].(map[string]any)["query"] == nil {
		t.Fatalf("stat: %v", stat)
	}

	ver, err := run(t, "actions", "results", "verify")
	if err != nil {
		t.Fatal(err)
	}
	if ver["ok"] != true || ver["missing"].(float64) != 0 {
		t.Fatalf("verify: %v", ver)
	}

	// 默认 dry-run：只计划，不删除
	plan, err := run(t, "actions", "results", "gc", "--older-than", "0", "--keep-last", "1")
	if err != nil {
		t.Fatal(err)
	}
	if plan["dry_run"] != true || len(plan["would_delete"].([]any)) != 1 {
		t.Fatalf("gc dry-run: %v", plan)
	}
	results, _ := run(t, "actions", "results", "list")
	for _, s := range results["results"].([]any) {
		f := s.(map[string]any)["files"].([]any)[0].(map[string]any)
		if _, err := os.Stat(f["abs_path"].(string)); err != nil {
			t.Fatalf("dry-run deleted %s", f["abs_path"])
		}
	}

	// --apply 才删除；keep-last 保护最新
	if _, err := run(t, "actions", "results", "gc", "--older-than", "0", "--keep-last", "1", "--apply"); err != nil {
		t.Fatal(err)
	}
	ver, err = run(t, "actions", "results", "verify")
	if err != nil {
		t.Fatal(err)
	}
	if ver["ok"] != false || ver["missing"].(float64) != 1 {
		t.Fatalf("verify after gc: %v", ver)
	}

	if got, err := run(t, "actions", "results", "list", "--type", "convert"); err == nil || got["code"].(float64) != 2 {
		t.Fatalf("convert should be rejected: %v %v", got, err)
	}
}

func TestActionsResultsAllowedInStrongPrivacy(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeJSON(t, filepath.Join(dir, "d.json"), `[{"region":"A","n":1}]`)
	if _, err := run(t, "query", "--sql", `SELECT region, n FROM "d.json"`); err != nil {
		t.Fatal(err)
	}

	// 强隐私只限制读内容；结果集元数据/统计/校验仍可用
	for _, args := range [][]string{
		{"actions", "results", "list"},
		{"actions", "results", "stat"},
		{"actions", "results", "verify"},
		{"actions", "results", "gc", "--older-than", "0", "--keep-last", "1"},
	} {
		if _, err := runRaw(t, args...); err != nil {
			t.Errorf("%v: %v", args, err)
		}
	}
}
