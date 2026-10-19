package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDatasetsCreateFromResult(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeJSON(t, filepath.Join(dir, "d.json"), `[{"region":"A","n":1},{"region":"B","n":2}]`)

	q, err := run(t, "query", "--sql", `SELECT region, n FROM "d.json"`)
	if err != nil {
		t.Fatal(err)
	}
	sourceID := q["action_id"].(string)

	created, err := run(t, "datasets", "create", "promoted", "--result", "action:"+sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if created["name"] != "promoted" || created["record_count"].(float64) != 2 ||
		created["data_file"] != ".dtool/datasets/promoted/data.json" || created["schema_file"] == nil {
		t.Fatalf("create: %v", created)
	}

	show, err := run(t, "datasets", "show", "promoted")
	if err != nil {
		t.Fatal(err)
	}
	cols := show["schema"].(map[string]any)["columns"].([]any)
	if len(cols) != 2 {
		t.Fatalf("schema: %v", show)
	}
	for _, c := range cols {
		if _, ok := c.(map[string]any)["samples"]; ok {
			t.Fatalf("promoted schema 不应含 samples: %v", c)
		}
	}

	// 结果集转换出的数据集可被 SQL 直接引用
	got, err := run(t, "query", "--rows", "--source", "p=dataset:promoted", "--sql", `SELECT COUNT(*) AS n FROM p`)
	if err != nil || got["rows"].([]any)[0].(map[string]any)["n"].(float64) != 2 {
		t.Fatalf("query promoted: %v %v", got, err)
	}

	// latest:query 也可以作为来源
	if _, err := run(t, "datasets", "create", "latest_copy", "--result", "latest:query"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".dtool", "datasets", "latest_copy", "data.schema.json")); err != nil {
		t.Fatal("latest_copy schema missing")
	}

	// 强隐私下仍然允许：数据只在本地流转，stdout 只给 data/schema 文件位置
	got, err = runRaw(t, "datasets", "create", "strong_copy", "--result", "action:"+sourceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"record_count", "columns"} {
		if _, ok := got[k]; ok {
			t.Fatalf("强隐私 create 不应输出 %q: %v", k, got)
		}
	}
	if arts := got["artifacts"].([]any); len(arts) != 2 {
		t.Fatalf("strong create artifacts: %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".dtool", "datasets", "strong_copy", "data.json")); err != nil {
		t.Fatal("strong_copy data missing")
	}
}
