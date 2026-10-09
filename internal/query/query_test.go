package query

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dezhishen/dtool/pkg/types"
)

const sample = `[{"region":"North","amount":100},{"region":"South","amount":250.5},{"region":"North","amount":50}]`

func setup(t *testing.T) (ws, cwd string) {
	t.Helper()
	root := t.TempDir()
	ws = filepath.Join(root, ".dtool")
	if err := os.MkdirAll(filepath.Join(ws, "outputs", "abc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "outputs", "abc", "data.json"), []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "local.json"), []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	return ws, root
}

func opts(ws, cwd, sql string) Options {
	return Options{SQL: sql, Roots: []string{ws, cwd}, Sandbox: true, MaxRows: 100, Timeout: 10 * time.Second}
}

func mustRun(t *testing.T, o Options) *types.QueryResult {
	t.Helper()
	r, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunWithAliasAndTypes(t *testing.T) {
	ws, cwd := setup(t)
	o := opts(ws, cwd, `SELECT region, SUM(amount) AS total FROM sales GROUP BY region ORDER BY region`)
	o.Sources = map[string]string{"sales": filepath.Join(ws, "outputs", "abc", "data.json")}
	r := mustRun(t, o)
	if r.RowCount != 2 || strings.Join(r.Columns, ",") != "region,total" {
		t.Fatalf("result: %+v", r)
	}
	if v, _ := r.Rows[0].Get("total"); v != 150.0 {
		t.Fatalf("North total = %#v", v)
	}
	if v, _ := r.Rows[1].Get("total"); v != 250.5 {
		t.Fatalf("South total = %#v", v)
	}
}

func TestRelativeNamesResolveToWorkspaceThenCwd(t *testing.T) {
	ws, cwd := setup(t)
	// 仅当前目录存在 local.json
	r := mustRun(t, opts(ws, cwd, `SELECT COUNT(*) AS n FROM "local.json"`))
	if v, _ := r.Rows[0].Get("n"); v != int64(3) {
		t.Fatalf("n = %#v", v)
	}
	// 相对工作区 outputs/ 解析
	r = mustRun(t, opts(ws, cwd, "SELECT COUNT(*) AS n FROM `abc/data.json`"))
	if v, _ := r.Rows[0].Get("n"); v != int64(3) {
		t.Fatalf("n = %#v", v)
	}
	// 未加引号的文件名同样会被解析
	r = mustRun(t, opts(ws, cwd, `SELECT COUNT(*) AS n FROM abc/data.json`))
	if v, _ := r.Rows[0].Get("n"); v != int64(3) {
		t.Fatalf("unquoted n = %#v", v)
	}
	// 同一文件多次引用只载入一次
	r = mustRun(t, opts(ws, cwd, `SELECT COUNT(*) AS n FROM "local.json" a JOIN "local.json" b ON a.region = b.region`))
	if v, _ := r.Rows[0].Get("n"); v != int64(5) {
		t.Fatalf("self join n = %#v", v)
	}
}

func TestSandboxViolations(t *testing.T) {
	ws, cwd := setup(t)
	outside := filepath.Join(t.TempDir(), "secret.json")
	os.WriteFile(outside, []byte(sample), 0o644)

	bad := []string{
		`SELECT * FROM "` + outside + `"`,
		`SELECT * FROM "../` + filepath.Base(filepath.Dir(outside)) + `/secret.json"`,
		`DELETE FROM "local.json"`,
		`DROP TABLE x`,
		`SELECT 1; SELECT 2`,
		``,
	}
	for _, sql := range bad {
		if _, _, err := Rewrite(opts(ws, cwd, sql)); err == nil {
			t.Errorf("sandbox accepted %q", sql)
		}
	}
	good := []string{
		`SELECT amount / 2 FROM "local.json"`,
		`WITH t AS (SELECT * FROM "local.json") SELECT * FROM t;`,
		`SELECT '/etc/passwd' AS s, 'a;b' AS u FROM "local.json"`,
		`select "region" AS "my col" from "local.json"`,
		`SELECT amount/2 FROM "local.json"`,
	}
	for _, sql := range good {
		if _, _, err := Rewrite(opts(ws, cwd, sql)); err != nil {
			t.Errorf("sandbox rejected %q: %v", sql, err)
		}
	}
}

// 未加引号的越界路径不会被当作表读取：内存库不碰文件系统，只会得到语法错误。
func TestUnquotedPathsNeverReadFiles(t *testing.T) {
	ws, cwd := setup(t)
	outside := filepath.Join(t.TempDir(), "secret.json")
	os.WriteFile(outside, []byte(sample), 0o644)
	for _, sql := range []string{
		`SELECT * FROM "local.json", ` + outside,
		`SELECT * FROM "local.json", ../x/y.json`,
		`SELECT * FROM ` + outside,
	} {
		if _, err := Run(context.Background(), opts(ws, cwd, sql)); err == nil {
			t.Errorf("expected error for %q", sql)
		}
	}
}

func TestSandboxOffAllowsOutsideFile(t *testing.T) {
	ws, cwd := setup(t)
	outside := filepath.Join(t.TempDir(), "secret.json")
	os.WriteFile(outside, []byte(sample), 0o644)
	o := opts(ws, cwd, `SELECT COUNT(*) AS n FROM "`+outside+`"`)
	o.Sandbox = false
	if v, _ := mustRun(t, o).Rows[0].Get("n"); v != int64(3) {
		t.Fatalf("n = %#v", v)
	}
}

func TestExplicitSourceBypassesSandboxRoots(t *testing.T) {
	ws, cwd := setup(t)
	outside := filepath.Join(t.TempDir(), "ext.json")
	os.WriteFile(outside, []byte(sample), 0o644)
	o := opts(ws, cwd, `SELECT COUNT(*) AS n FROM ext`)
	o.Sources = map[string]string{"ext": outside}
	if v, _ := mustRun(t, o).Rows[0].Get("n"); v != int64(3) {
		t.Fatalf("n = %#v", v)
	}
}

func TestMaxRowsAndErrors(t *testing.T) {
	ws, cwd := setup(t)
	o := opts(ws, cwd, `SELECT * FROM "local.json"`)
	o.MaxRows = 2
	_, err := Run(context.Background(), o)
	var te *types.Error
	if !errors.As(err, &te) || !strings.Contains(te.Message, "max-rows") || te.Hint == "" {
		t.Fatalf("err = %v", err)
	}

	_, err = Run(context.Background(), opts(ws, cwd, `SELECT * FROM "nonexistent.json"`))
	if !errors.As(err, &te) || te.Code != types.CodeExec || te.Hint == "" {
		t.Fatalf("missing table err = %v (hint should explain quoting)", err)
	}

	_, err = Run(context.Background(), opts(ws, cwd, `SELECT FROM WHERE`))
	if err == nil {
		t.Fatal("syntax error not reported")
	}
}

func TestEmptyResultIsNonNil(t *testing.T) {
	ws, cwd := setup(t)
	r := mustRun(t, opts(ws, cwd, `SELECT region FROM "local.json" WHERE amount > 1000`))
	if r.Rows == nil || r.RowCount != 0 || r.Columns == nil {
		t.Fatalf("result: %+v", r)
	}
}

func TestCanceledContext(t *testing.T) {
	ws, cwd := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, opts(ws, cwd, `SELECT * FROM "local.json"`)); err == nil {
		t.Fatal("canceled context ignored")
	}
}

func TestNumericColumnsCompareAndSortNumerically(t *testing.T) {
	ws, cwd := setup(t)
	// 混合整数/小数，且 opt 列首行为 null：都必须按数值比较与排序
	p := filepath.Join(cwd, "n.json")
	os.WriteFile(p, []byte(`[{"id":9,"amount":100,"opt":null},{"id":10,"amount":250.5,"opt":7},{"id":100,"amount":50,"opt":12}]`), 0o644)

	r := mustRun(t, opts(ws, cwd, `SELECT id FROM "n.json" WHERE amount > 60 ORDER BY id`))
	if r.RowCount != 2 {
		t.Fatalf("amount > 60 returned %d rows: %+v", r.RowCount, r.Rows)
	}
	if v, _ := r.Rows[0].Get("id"); v != int64(9) {
		t.Fatalf("first id = %#v, want 9 (numeric order)", v)
	}
	r = mustRun(t, opts(ws, cwd, `SELECT id FROM "n.json" WHERE opt > 8`))
	if r.RowCount != 1 {
		t.Fatalf("opt > 8 returned %d rows", r.RowCount)
	}
	r = mustRun(t, opts(ws, cwd, `SELECT id FROM "n.json" ORDER BY id DESC LIMIT 1`))
	if v, _ := r.Rows[0].Get("id"); v != int64(100) {
		t.Fatalf("max id = %#v", v)
	}
}

func TestValueKinds(t *testing.T) {
	ws, cwd := setup(t)
	p := filepath.Join(cwd, "k.json")
	os.WriteFile(p, []byte(`[{"s":"x","f":true,"m":1,"n":{"a":1}},{"s":"y","f":false,"m":"two","n":[1]}]`), 0o644)
	r := mustRun(t, opts(ws, cwd, `SELECT s, f, m, n FROM "k.json" ORDER BY s`))
	if v, _ := r.Rows[0].Get("f"); v != int64(1) {
		t.Errorf("bool = %#v", v)
	}
	if v, _ := r.Rows[1].Get("m"); v != "two" {
		t.Errorf("mixed column = %#v", v)
	}
	if v, _ := r.Rows[0].Get("n"); v != `{"a":1}` {
		t.Errorf("nested = %#v", v)
	}
}

func TestEmptyAndInvalidSources(t *testing.T) {
	ws, cwd := setup(t)
	os.WriteFile(filepath.Join(cwd, "e.json"), []byte(`[]`), 0o644)
	os.WriteFile(filepath.Join(cwd, "bad.json"), []byte(`{"a":1}`), 0o644)
	r := mustRun(t, opts(ws, cwd, `SELECT COUNT(*) AS n FROM "e.json"`))
	if v, _ := r.Rows[0].Get("n"); v != int64(0) {
		t.Fatalf("n = %#v", v)
	}
	if _, err := Run(context.Background(), opts(ws, cwd, `SELECT * FROM "bad.json"`)); err == nil {
		t.Fatal("non-array source accepted")
	}
}

func TestLookupBindsDatasetNamesInFromAndJoinOnly(t *testing.T) {
	ws, cwd := setup(t)
	data := filepath.Join(ws, "outputs", "abc", "data.json")
	lookup := func(n string) (string, bool) { return data, n == "sales" }

	o := opts(ws, cwd, `SELECT COUNT(*) AS n FROM sales`)
	o.Lookup = lookup
	if v, _ := mustRun(t, o).Rows[0].Get("n"); v != int64(3) {
		t.Fatalf("bare: %#v", v)
	}
	o.SQL = `SELECT COUNT(*) AS n FROM "sales" a JOIN sales b ON a.region = b.region`
	if v, _ := mustRun(t, o).Rows[0].Get("n"); v != int64(5) {
		t.Fatalf("quoted+join: %#v", v)
	}

	// 非 FROM/JOIN 位置的同名标识符（如列别名）不会触发载入
	o.SQL = `SELECT 1 AS "sales"`
	_, binds, err := Rewrite(o)
	if err != nil || len(binds) != 0 {
		t.Fatalf("alias bound a dataset: %v %v", binds, err)
	}
	// 显式 --source 优先于数据集
	o.SQL = `SELECT COUNT(*) AS n FROM sales`
	other := filepath.Join(cwd, "local.json")
	o.Sources = map[string]string{"sales": other}
	_, binds, _ = Rewrite(o)
	if len(binds) != 1 || binds[0].Path != other {
		t.Fatalf("binds = %+v", binds)
	}
}
