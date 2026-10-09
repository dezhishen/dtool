package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dezhishen/dtool/internal/action"
	"github.com/dezhishen/dtool/internal/dataset"
	"github.com/dezhishen/dtool/internal/workspace"
	"github.com/dezhishen/dtool/pkg/types"
	"github.com/xuri/excelize/v2"
)

func newEnv(t *testing.T) (*Env, string) {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	ws, err := workspace.Open(filepath.Join(dir, ".dtool"))
	if err != nil {
		t.Fatal(err)
	}
	return &Env{Ctx: context.Background(), WS: ws, Rec: &action.Recorder{WS: ws}, Preview: 20, Command: "dtool test"}, dir
}

func writeXlsx(t *testing.T, dir string) string {
	t.Helper()
	f := excelize.NewFile()
	rows := [][]any{
		{"id", "region", "amount", "code", "when"},
		{1, "North", 100, "00123", "2026-01-15"},
		{2, "South", 250.5, "00456", "2026-02-03"},
		{3, "North", 50, "00789", "2026-03-09"},
	}
	for i, r := range rows {
		cell, _ := excelize.CoordinatesToCellName(1, i+1)
		if err := f.SetSheetRow("Sheet1", cell, &r); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(dir, "data.xlsx")
	if err := f.SaveAs(p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPipelineEndToEnd(t *testing.T) {
	env, dir := newEnv(t)
	xlsx := writeXlsx(t, dir)

	res, err := env.Pipeline(Config{Excel: xlsx, SQL: `SELECT region, SUM(amount) AS total FROM data GROUP BY region ORDER BY region`,
		Chart: "bar", X: "region", Y: "total", Format: "svg", MaxRows: 100, Timeout: 30 * time.Second, Sandbox: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Query.RowCount != 2 || res.Convert.RecordCount != 3 {
		t.Fatalf("unexpected counts: %+v %+v", res.Query, res.Convert)
	}
	if _, err := os.Stat(env.WS.Abs(res.Chart.File)); err != nil {
		t.Fatal(err)
	}
	parent, err := env.Rec.Get(res.ActionID)
	if err != nil || parent.Status != action.StatusSuccess {
		t.Fatalf("parent: %v %+v", err, parent)
	}
	up, down, err := env.Rec.Trace(res.Convert.ActionID)
	if err != nil || len(up) != 1 || len(down) < 2 {
		t.Fatalf("trace up=%d down=%d err=%v", len(up), len(down), err)
	}

	// 前导零保持 string；schema 文件存在
	conv, _ := env.Rec.Get(res.Convert.ActionID)
	if conv.Output.SchemaRef == "" {
		t.Fatal("schema missing")
	}
	if v, _ := conv.Output.Preview[0].Get("code"); v != "00123" {
		t.Fatalf("code = %#v", v)
	}

	// 派生查询 + 注释 + latest 引用
	q2, err := env.Query(QueryParams{SQL: `SELECT SUM(total) AS grand FROM prev`, Sources: map[string]string{"prev": "latest:query"},
		From: "action:" + res.Query.ActionID, Sandbox: true, MaxRows: 10}, "")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := q2.Rows[0].Get("grand"); v == nil {
		t.Fatal("grand nil")
	}
	d, _ := env.Rec.Get(q2.ActionID)
	if d.DerivedFrom != res.Query.ActionID {
		t.Fatalf("derived_from = %q", d.DerivedFrom)
	}
	if _, err := env.Rec.Annotate(q2.ActionID, "含税", "user"); err != nil {
		t.Fatal(err)
	}
}

func TestSandboxAndFailures(t *testing.T) {
	env, _ := newEnv(t)
	for _, sql := range []string{
		`SELECT * FROM "/etc/passwd"`,
		`SELECT * FROM a, /etc/passwd`,
		`DELETE FROM x`,
		`SELECT 1; SELECT 2`,
	} {
		if _, err := env.Query(QueryParams{SQL: sql, Sandbox: true}, ""); err == nil {
			t.Errorf("expected sandbox error for %q", sql)
		}
	}
	// 失败也要被记录
	entries, _, _ := env.Rec.List(action.Filter{Status: action.StatusFailed})
	if len(entries) != 4 {
		t.Errorf("failed actions = %d, want 4", len(entries))
	}
	if _, err := env.Rec.Get("../../etc/passwd"); err == nil {
		t.Error("path traversal id accepted")
	}
}

func TestConvertOnlyThenReuse(t *testing.T) {
	env, dir := newEnv(t)
	xlsx := writeXlsx(t, dir)

	res, err := env.Pipeline(Config{Excel: xlsx})
	if err != nil || res.Query != nil || res.Chart != nil || res.Convert == nil {
		t.Fatalf("convert-only: %v %+v", err, res)
	}
	// 数据已持久化到磁盘，之后可直接复用，无需重新转换
	res2, err := env.Pipeline(Config{Input: "latest:convert", SQL: `SELECT COUNT(*) AS n FROM data`,
		MaxRows: 10, Sandbox: true})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := res2.Query.Rows[0].Get("n"); v == nil || res2.Convert != nil {
		t.Fatalf("reuse: %+v", res2)
	}
	q, _ := env.Rec.Get(res2.Query.ActionID)
	if q.DerivedFrom != res.Convert.ActionID {
		t.Fatalf("derived_from = %q", q.DerivedFrom)
	}
	if _, err := env.Pipeline(Config{Input: "latest:convert", Chart: "bar"}); err == nil {
		t.Fatal("chart without sql should fail")
	}
}

func TestDatasetsAreIndependentOfActions(t *testing.T) {
	env, dir := newEnv(t)
	xlsx := writeXlsx(t, dir) // data.xlsx

	c1, err := env.Convert(ConvertParams{Input: xlsx}, "")
	if err != nil {
		t.Fatal(err)
	}
	if c1.Name != "data" {
		t.Fatalf("name = %q", c1.Name)
	}
	// 数据集直接位于 .dtool/datasets/<name>/，路径里没有 action id，也不在 outputs/ 下
	if c1.DataFile != ".dtool/datasets/data/data.json" || c1.SchemaFile != ".dtool/datasets/data/data.schema.json" {
		t.Fatalf("paths: %s %s", c1.DataFile, c1.SchemaFile)
	}
	if strings.Contains(c1.DataFile, c1.ActionID) || strings.Contains(c1.DataFile, "outputs") {
		t.Fatal("dataset path is tied to an action")
	}
	if _, err := os.Stat(filepath.Join(dir, ".dtool", "outputs", "convert")); err == nil {
		t.Fatal("convert must not create an action output dir")
	}

	// Schema 必有，且记录更新日期，与数据集元数据一致
	readSchema := func(c *types.ConvertResult) map[string]any {
		var doc map[string]any
		if err := workspace.ReadJSON(env.WS.Abs(c.SchemaFile), &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	sch1 := readSchema(c1)
	if sch1["updated_at"] != c1.UpdatedAt.UTC().Format(time.RFC3339) || sch1["generated_at"] != nil {
		t.Fatalf("schema dates: %v vs %v", sch1["updated_at"], c1.UpdatedAt)
	}

	// 同名重新转换：覆盖数据与 Schema，更新日期刷新，无版本概念
	time.Sleep(1100 * time.Millisecond)
	c2, err := env.Convert(ConvertParams{Input: xlsx, Name: "data"}, "")
	if err != nil || c2.DataFile != c1.DataFile || c2.SchemaFile != c1.SchemaFile {
		t.Fatalf("reconvert: %+v %v", c2, err)
	}
	if !c2.UpdatedAt.After(c1.UpdatedAt) || readSchema(c2)["updated_at"] == sch1["updated_at"] {
		t.Fatal("updated_at not refreshed")
	}
	if _, err := os.Stat(filepath.Join(env.WS.DatasetsDir(), "data", "dataset.json")); err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	workspace.ReadJSON(filepath.Join(env.WS.DatasetsDir(), "data", "dataset.json"), &meta)
	if _, has := meta["version"]; has {
		t.Fatal("dataset.json must not carry a version")
	}
	c3, err := env.Convert(ConvertParams{Input: xlsx, Name: "sales 2026"}, "")
	if err != nil || c3.Name != "sales_2026" {
		t.Fatalf("named: %+v %v", c3, err)
	}

	// 暂存目录不残留
	entries, _ := os.ReadDir(env.WS.DatasetsDir())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("leftover %s", e.Name())
		}
	}

	// 数据集名可直接当表名；血缘指向产出当前版本的 Action
	q, err := env.Query(QueryParams{SQL: `SELECT COUNT(*) AS n FROM data`, Sandbox: true, MaxRows: 5, From: "dataset:data"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := q.Rows[0].Get("n"); v != int64(3) {
		t.Fatalf("n = %#v", v)
	}
	qa, _ := env.Rec.Get(q.ActionID)
	if qa.DerivedFrom != c2.ActionID {
		t.Fatalf("derived_from = %s, want %s", qa.DerivedFrom, c2.ActionID)
	}
	if !strings.HasPrefix(q.ResultFile, ".dtool/outputs/queries/"+q.ActionID+"/") {
		t.Fatalf("query result path: %s", q.ResultFile)
	}
	ch, err := env.Visualize(VisualizeParams{Input: "dataset:data", Type: "table"}, "")
	if err != nil || !strings.HasPrefix(ch.File, ".dtool/outputs/charts/"+ch.ActionID+"/") {
		t.Fatalf("chart: %+v %v", ch, err)
	}

	// 数据集独立存在：删除所有 Action 记录后仍可列出、引用
	os.RemoveAll(env.WS.ActionsDir())
	os.Remove(env.WS.IndexPath())
	os.MkdirAll(env.WS.ActionsDir(), 0o755)
	ds := &dataset.Store{WS: env.WS}
	if items, _ := ds.List(); len(items) != 2 {
		t.Fatalf("datasets after wiping actions: %+v", items)
	}
	if _, err := env.Rec.Resolve("dataset:data"); err != nil {
		t.Fatal(err)
	}
}

func TestConvertMemoryGuard(t *testing.T) {
	env, dir := newEnv(t)
	xlsx := writeXlsx(t, dir)

	tiny := uint64(1024)
	env.MaxMemory = &tiny
	_, err := env.Convert(ConvertParams{Input: xlsx}, "")
	var te *types.Error
	if !errors.As(err, &te) || te.Code != types.CodeExec || !strings.Contains(te.Message, "Excel 文件") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(te.Detail, "固定开销") || !strings.Contains(te.Detail, "× 6") ||
		!strings.Contains(te.Hint, "--max-memory 0") {
		t.Fatalf("not actionable: %q / %q", te.Detail, te.Hint)
	}
	// --load-mode 只对 query 生效，发给 convert 是误导
	if strings.Contains(te.Hint, "--load-mode") {
		t.Fatalf("convert 不应建议 --load-mode: %q", te.Hint)
	}
	if !strings.Contains(te.Hint, "拆分") {
		t.Fatalf("convert 应建议拆分输入: %q", te.Hint)
	}
	failed, _, err := env.Rec.List(action.Filter{Status: action.StatusFailed})
	if err != nil || len(failed) == 0 {
		t.Fatalf("内存不足也应记录失败的 Action：%v %v", failed, err)
	}

	off := uint64(0)
	env.MaxMemory = &off
	if _, err := env.Convert(ConvertParams{Input: xlsx, Name: "ok"}, ""); err != nil {
		t.Fatalf("guard off: %v", err)
	}
}

func TestDefaultDatasetName(t *testing.T) {
	cases := []struct{ in, sheet, want string }{
		{"/x/销量表.xlsx", "", "销量表"},
		{"data.xlsx", "Q4 data", "data_Q4_data"},
		{"a.b.xlsx", "", "a.b"},
	}
	for _, c := range cases {
		if got := DefaultDatasetName(c.in, c.sheet); got != c.want {
			t.Errorf("%q/%q = %q, want %q", c.in, c.sheet, got, c.want)
		}
	}
}

func TestExcelExport(t *testing.T) {
	env, dir := newEnv(t)
	xlsxIn := writeXlsx(t, dir)
	if _, err := env.Convert(ConvertParams{Input: xlsxIn}, ""); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out", "report.xlsx")
	q, err := env.Query(QueryParams{SQL: `SELECT region, SUM(amount) AS total FROM d GROUP BY region ORDER BY region`,
		Sources: map[string]string{"d": "dataset:data"}, Format: "xlsx", Output: out, Sandbox: true, MaxRows: 10}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(q.OutputFile, "out/report.xlsx") {
		t.Fatalf("output_file = %s", q.OutputFile)
	}
	f, err := excelize.OpenFile(out)
	if err != nil {
		t.Fatalf("not a valid xlsx: %v", err)
	}
	defer f.Close()
	if rows, _ := f.GetRows("Sheet1"); len(rows) != 3 || rows[1][0] != "North" || rows[1][1] != "150" {
		t.Fatalf("rows = %v", rows)
	}
	// 记录在 Action 中，且 result.json 仍是主产物
	a, _ := env.Rec.Get(q.ActionID)
	if len(a.Output.Files) != 2 || !strings.HasSuffix(a.Output.Files[0], "result.json") {
		t.Fatalf("files = %v", a.Output.Files)
	}

	// 表格导出为 xlsx（visualize --type table）
	v, err := env.Visualize(VisualizeParams{Input: "action:" + q.ActionID, Type: "table", Format: "xlsx"}, "")
	if err != nil || !strings.HasSuffix(v.File, ".xlsx") {
		t.Fatalf("table xlsx: %+v %v", v, err)
	}
	if _, err := excelize.OpenFile(env.WS.Abs(v.File)); err != nil {
		t.Fatal(err)
	}
}

func TestXLSXFormatValidation(t *testing.T) {
	env, dir := newEnv(t)
	xlsxIn := writeXlsx(t, dir)
	env.Convert(ConvertParams{Input: xlsxIn}, "")
	if _, err := env.Query(QueryParams{SQL: `SELECT 1`, Format: "xlsx", Sandbox: true}, ""); err == nil {
		t.Fatal("xlsx without --output must be rejected (binary to stdout)")
	}
	q, _ := env.Query(QueryParams{SQL: `SELECT 1 AS x`, Sandbox: true}, "")
	for _, typ := range []string{"bar", "pie"} {
		if _, err := env.Visualize(VisualizeParams{Input: "action:" + q.ActionID, Type: typ, X: "x", Y: "x", Format: "xlsx"}, ""); err == nil {
			t.Errorf("%s accepted xlsx", typ)
		}
	}
}
