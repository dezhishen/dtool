package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dezhishen/dtool/internal/memguard"
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

func TestTimeoutAppliesToQueryNotLoading(t *testing.T) {
	ws, cwd := setup(t)
	// 极小的 --timeout：载入照常完成（否则不会出现表），失败归因于查询阶段
	_, err := Run(context.Background(), Options{SQL: `SELECT COUNT(*) AS n FROM d`, Roots: []string{ws, cwd},
		Sandbox: true, Sources: map[string]string{"d": filepath.Join(cwd, "local.json")},
		LoadMode: "stream", Timeout: time.Nanosecond, MaxRows: 10})
	var te *types.Error
	if !errors.As(err, &te) || !strings.Contains(te.Message, "query timeout") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(te.Message, "载入超时") {
		t.Fatalf("载入不应受 --timeout 限制: %v", err)
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

func TestMemoryGuardBlocksOversizedLoad(t *testing.T) {
	ws, cwd := setup(t)
	base := func(max *uint64) Options {
		return Options{SQL: `SELECT COUNT(*) AS n FROM d`, Roots: []string{ws, cwd}, Sandbox: true,
			Sources: map[string]string{"d": filepath.Join(cwd, "local.json")}, MaxMemory: max}
	}
	tiny := uint64(10)
	_, err := Run(context.Background(), base(&tiny))
	var te *types.Error
	if !errors.As(err, &te) || te.Code != types.CodeExec || !strings.Contains(te.Message, "内存不足") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(te.Detail, "13 倍") || !strings.Contains(te.Hint, "--max-memory 0") {
		t.Fatalf("not actionable: %q / %q", te.Detail, te.Hint)
	}
	// --max-memory 0 关闭检查后应正常执行
	off := uint64(0)
	r, err := Run(context.Background(), base(&off))
	if err != nil || r.RowCount != 1 {
		t.Fatalf("guard off: %+v %v", r, err)
	}
	// 预算充足时也放行
	big := uint64(1 << 30)
	if _, err := Run(context.Background(), base(&big)); err != nil {
		t.Fatalf("enough memory: %v", err)
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

const mixedJSON = `[
 {"id":1,"金额":10,"比率":0.5,"名称":"甲","flag":true,"ext":{"a":1},"tags":[1,2]},
 {"id":2,"金额":20.5,"比率":1,"名称":"乙","flag":false,"ext":{"b":2},"tags":[]},
 {"id":3,"金额":30,"比率":2.5,"名称":"丙","flag":true,"ext":null,"tags":[3]},
 {"id":4,"金额":null,"名称":"丁"}
]`

func TestStreamModeMatchesFullMode(t *testing.T) {
	ws, cwd := setup(t)
	p := filepath.Join(cwd, "mixed.json")
	if err := os.WriteFile(p, []byte(mixedJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	queries := []string{
		`SELECT * FROM d ORDER BY id`,
		`SELECT 名称, 金额, 比率, flag, ext, tags FROM d ORDER BY id`,
		`SELECT COUNT(*) AS n, SUM(金额) AS s, AVG(比率) AS a FROM d`,
		`SELECT id FROM d WHERE 金额 > 25`,    // 数值比较（流式模式也必须按数值比较）
		`SELECT id FROM d WHERE 金额 IS NULL`, // 空值
		`SELECT id, flag, tags FROM d WHERE ext IS NOT NULL ORDER BY id`,
	}
	var full []string
	for i, sql := range queries {
		run := func(mode string) string {
			r, err := Run(context.Background(), Options{SQL: sql, Roots: []string{ws, cwd}, Sandbox: true,
				Sources: map[string]string{"d": p}, LoadMode: mode, MaxRows: 100})
			if err != nil {
				t.Fatalf("mode=%s sql=%s: %v", mode, sql, err)
			}
			b, err := json.Marshal(struct {
				Cols []string
				Rows []types.Row
				N    int
			}{r.Columns, r.Rows, r.RowCount})
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
		got, want := run("stream"), run("full")
		if got != want {
			t.Errorf("query %d 结果不一致 | stream: %s | full: %s", i, got, want)
		}
		full = append(full, want)
	}
	// 列顺序按首次出现，且类型推断与整块解析一致
	if !strings.Contains(full[0], `["id","金额","比率","名称","flag","ext","tags"]`) {
		t.Fatalf("列顺序不符：%s", full[0])
	}
	r, err := Run(context.Background(), Options{SQL: `SELECT id FROM d WHERE 金额 > 25`, Roots: []string{ws, cwd},
		Sandbox: true, Sources: map[string]string{"d": p}, LoadMode: "stream", MaxRows: 10})
	if err != nil || r.RowCount != 1 {
		t.Fatalf("流式模式数值比较错误：%+v %v", r, err)
	}
	if v, _ := r.Rows[0].Get("id"); v != int64(3) {
		t.Fatalf("id = %#v", v)
	}
}

func TestParseLoadModeAndResolve(t *testing.T) {
	for _, s := range []string{"", "auto", "STREAM", " stream "} {
		if _, err := ParseLoadMode(s); err != nil {
			t.Errorf("ParseLoadMode(%q) = %v", s, err)
		}
	}
	for _, s := range []string{"fast", "1", "full "} {
		_, err := ParseLoadMode(s)
		var te *types.Error
		if s == "full " {
			if err != nil {
				t.Errorf("ParseLoadMode(%q) should trim: %v", s, err)
			}
			continue
		}
		if !errors.As(err, &te) || te.Code != types.CodeUsage || te.Hint == "" {
			t.Errorf("ParseLoadMode(%q) err = %v", s, err)
		}
	}

	big, small := uint64(64<<20), uint64(1<<20)
	none := memguard.Memory{}
	cases := []struct {
		mode string
		size uint64
		mem  memguard.Memory
		want LoadMode
	}{
		{"stream", small, none, LoadStream},
		{"full", big, none, LoadFull},
		{"auto", small, none, LoadFull},
		{"auto", big, none, LoadStream},
		{"auto", small, memguard.Memory{Available: 1 << 20, Source: "t"}, LoadStream}, // 预算不足 -> 转流式
		{"auto", small, memguard.Memory{Available: 1 << 30, Source: "t"}, LoadFull},
	}
	for _, c := range cases {
		if got := resolveMode(c.mode, c.size, c.mem); got != c.want {
			t.Errorf("resolveMode(%s, %d) = %s, want %s", c.mode, c.size, got, c.want)
		}
	}
	if peakFactor(LoadStream) >= peakFactor(LoadFull) {
		t.Fatal("流式倍率应当更低")
	}
}

func TestStreamModeRejectsBadInput(t *testing.T) {
	ws, cwd := setup(t)
	files := map[string]string{
		"object.json":    `{"a":1}`,
		"scalar.json":    `[1,2]`,
		"trailing.json":  `[{"a":1}] []`,
		"truncated.json": `[{"a":1},`,
		"notjson.json":   `nope`,
	}
	for name, body := range files {
		p := filepath.Join(cwd, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, mode := range []string{"stream", "full"} {
			_, err := Run(context.Background(), Options{SQL: `SELECT COUNT(*) AS n FROM d`, Roots: []string{ws, cwd},
				Sandbox: true, Sources: map[string]string{"d": p}, LoadMode: mode, MaxRows: 10})
			var te *types.Error
			if !errors.As(err, &te) || te.Code != types.CodeExec {
				t.Errorf("%s/%s: err = %v", name, mode, err)
			}
		}
	}
}

func TestStreamModeHonorsCancellation(t *testing.T) {
	ws, cwd := setup(t)
	p := filepath.Join(cwd, "rows.json")
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < 2000; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"a":%d,"b":"x"}`, i)
	}
	sb.WriteString("]")
	if err := os.WriteFile(p, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, Options{SQL: `SELECT COUNT(*) AS n FROM d`, Roots: []string{ws, cwd}, Sandbox: true,
		Sources: map[string]string{"d": p}, LoadMode: "stream", MaxRows: 10}); err == nil {
		t.Fatal("已取消的 context 应当中断流式载入")
	}
}
