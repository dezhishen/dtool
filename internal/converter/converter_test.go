package converter

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dezhishen/dtool/pkg/types"
	"github.com/xuri/excelize/v2"
)

// writeSheet 生成一个只有一张表的工作簿。
func writeSheet(t *testing.T, dir, name string, rows [][]any) string {
	t.Helper()
	f := excelize.NewFile()
	for i, r := range rows {
		cell, err := excelize.CoordinatesToCellName(1, i+1)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.SetSheetRow("Sheet1", cell, &r); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, name)
	if err := f.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func codeOf(err error) int { return types.AsError(err, types.CodeGeneral).Code }

func TestRegistryBasics(t *testing.T) {
	if got := Names(); len(got) == 0 || got[0] != DefaultConverterName {
		t.Fatalf("已注册转换器 = %v，应包含 %s", got, DefaultConverterName)
	}
	exts := Extensions()
	if len(exts) != 1 || exts[0] != ".xlsx" {
		t.Fatalf("扩展名 = %v，期望 [.xlsx]", exts)
	}

	for _, name := range []string{"", "auto", "AUTO", " xlsx "} {
		c, err := Resolve(name, "报表.XLSX") // 扩展名与名字都大小写不敏感
		if err != nil {
			t.Fatalf("Resolve(%q) 出错：%v", name, err)
		}
		if c.Name() != DefaultConverterName {
			t.Fatalf("Resolve(%q).Name() = %s", name, c.Name())
		}
	}
}

func TestResolveErrors(t *testing.T) {
	if _, err := Resolve("", "data.csv"); codeOf(err) != types.CodeUsage {
		t.Fatalf("未知扩展名应报 CodeUsage，实际 %v", err)
	} else if hint := types.AsError(err, types.CodeUsage).Hint; !strings.Contains(hint, ".xlsx") {
		t.Fatalf("提示应列出支持的扩展名，实际 %q", hint)
	}
	if _, err := Resolve("nope", "a.xlsx"); codeOf(err) != types.CodeUsage {
		t.Fatalf("未知转换器应报 CodeUsage，实际 %v", err)
	}
	if _, err := Resolve("xlsx", "a.csv"); codeOf(err) != types.CodeUsage {
		t.Fatalf("名字与扩展名不匹配应报 CodeUsage，实际 %v", err)
	}
}

// stubConverter 是注册表用例的最小实现：只关心「每次解析是不是新实例」。
// xlsxConverter 是零大小类型，两个实例的指针可能相等（Go 允许零大小对象共用地址），
// 所以这里特意带一个字段。
type stubConverter struct{ configured int }

func (c *stubConverter) Name() string         { return "stub" }
func (c *stubConverter) Extensions() []string { return []string{".stub"} }
func (c *stubConverter) Configure(map[string]any) error {
	c.configured++
	return nil
}
func (c *stubConverter) Inspect(Options) (*Layout, error) { return &Layout{Format: c.Name()}, nil }
func (c *stubConverter) Rehearse(Options) (*Rehearsal, error) {
	return &Rehearsal{Layout: &Layout{Format: c.Name()}}, nil
}
func (c *stubConverter) Convert(Options) (*Result, error) { return &Result{}, nil }

func TestResolveReturnsFreshInstance(t *testing.T) {
	Register("stub", []string{".stub"}, func() Converter { return &stubConverter{} })
	defer unregister("stub")
	a, err := Resolve("stub", "a.stub")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Resolve("stub", "a.stub")
	if a == b {
		t.Fatal("每次解析都应新建实例，否则 Configure 会串味")
	}
	if _, err := Convert(Options{Converter: "stub", Input: "a.stub"}); err != nil {
		t.Fatal(err)
	}
	if a.(*stubConverter).configured != 0 {
		t.Fatal("Configure 只应作用在本次执行的新实例上")
	}
}

func TestRegisterPanics(t *testing.T) {
	mustPanic := func(name string, fn func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Fatalf("%s 应当 panic", name)
			}
		}()
		fn()
	}
	mustPanic("重名", func() { Register(DefaultConverterName, []string{".zzz"}, func() Converter { return &xlsxConverter{} }) })
	mustPanic("抢占扩展名", func() {
		Register("zzz", []string{".XLSX"}, func() Converter { return &xlsxConverter{} })
	})
	mustPanic("nil 工厂", func() { Register("nilc", []string{".n1"}, nil) })
	mustPanic("空扩展名", func() { Register("extless", nil, func() Converter { return &xlsxConverter{} }) })
}

func TestInspectTricky(t *testing.T) {
	dir := t.TempDir()
	path := writeTrickyXlsx(t, dir)
	l, err := Inspect(Options{Input: path})
	if err != nil {
		t.Fatal(err)
	}
	if l.Format != DefaultConverterName || l.Version != LayoutVersion {
		t.Fatalf("Layout 头信息不对：%+v", l)
	}
	if l.HeaderRow != 1 {
		t.Fatalf("header_row = %d，期望 1", l.HeaderRow)
	}
	if len(l.Columns) != 7 {
		t.Fatalf("列数 = %d，期望 7", len(l.Columns))
	}
	// 前导零的编号列必须被按文本处理，并且说明原因。
	var idCol *LayoutColumn
	for i := range l.Columns {
		if l.Columns[i].Name == "编号" {
			idCol = &l.Columns[i]
		}
	}
	if idCol == nil {
		t.Fatal("找不到 编号 列")
	}
	if idCol.Type != "string" || idCol.Note == "" {
		t.Fatalf("编号列 = %+v，期望 string 且带说明", *idCol)
	}
	// 合并单元格必须出现在问题或告警里。
	var merged bool
	for _, q := range l.Questions {
		if q.Code == "merged_cells" {
			merged = true
		}
	}
	if !merged {
		t.Fatalf("应提示合并单元格，问题 = %+v", l.Questions)
	}
	// 每列都要有置信度（0~1）与证据。
	for _, c := range l.Columns {
		if c.Confidence < 0 || c.Confidence > 1 || len(c.Evidence) == 0 {
			t.Fatalf("列 %s 缺少置信度/证据：%+v", c.Name, c)
		}
	}
}

func TestInspectTitleAndTotalRow(t *testing.T) {
	dir := t.TempDir()
	path := writeSheet(t, dir, "t.xlsx", [][]any{
		{"2026 年销售报表"},
		{"区域", "金额"},
		{"华东", 100},
		{"华南", 200},
		{"合计", 300},
	})
	l, err := Inspect(Options{Input: path})
	if err != nil {
		t.Fatal(err)
	}
	var headerQ, totalQ bool
	for _, q := range l.Questions {
		switch q.Code {
		case "header_row":
			headerQ = q.Row == 1
		case "total_row":
			totalQ = q.Row == 5
		}
	}
	if !headerQ || !totalQ {
		t.Fatalf("应提示标题行(1)与汇总行(5)，实际问题 = %+v", l.Questions)
	}
}

func TestInspectMultiSheet(t *testing.T) {
	dir := t.TempDir()
	f := excelize.NewFile()
	if err := f.SetSheetRow("Sheet1", "A1", &[]any{"区域", "金额"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.NewSheet("第二页"); err != nil {
		t.Fatal(err)
	}
	if err := f.SetSheetRow("第二页", "A1", &[]any{"区域", "金额"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "multi.xlsx")
	if err := f.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	l, err := Inspect(Options{Input: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Sheets) != 2 {
		t.Fatalf("sheets = %v", l.Sheets)
	}
	found := false
	for _, q := range l.Questions {
		if q.Code == "sheet" && len(q.Options) == 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("多工作表应提问，实际 = %+v", l.Questions)
	}
}

func TestConvertWithLayout(t *testing.T) {
	dir := t.TempDir()
	path := writeSheet(t, dir, "t.xlsx", [][]any{
		{"报表标题"},
		{"区域", "金额", "日期"},
		{"华东", 100, "2026-01-02"},
		{"华南", 200, "2026-01-03"},
	})
	// 交互式流程：第一次 inspect 把标题行当成了表头（并提问）；人确认「表头在第 2 行」，
	// 带着确认结果再 inspect 拿到真正的列，然后改列名/列类型，最后 convert。
	l, err := Inspect(Options{Input: path})
	if err != nil {
		t.Fatal(err)
	}
	if l.HeaderRow != 1 || len(l.Columns) != 1 {
		t.Fatalf("首次 inspect 期望按第 1 行探测出 1 列，实际 header=%d cols=%d", l.HeaderRow, len(l.Columns))
	}
	l.HeaderRow = 2
	l, err = Inspect(Options{Input: path, Layout: l})
	if err != nil {
		t.Fatal(err)
	}
	if l.HeaderRow != 2 || len(l.Columns) != 3 {
		t.Fatalf("带 Layout 再 inspect 应得到 3 列，实际 header=%d cols=%d", l.HeaderRow, len(l.Columns))
	}
	l.Columns[0].Name = "大区"
	l.Columns[2].Type = "string"

	out := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := Convert(Options{Input: path, OutDir: out, Layout: l, PreviewRows: 5})
	if err != nil {
		t.Fatal(err)
	}
	if res.RecordCount != 2 {
		t.Fatalf("行数 = %d，期望 2（表头之前的标题行应跳过）", res.RecordCount)
	}
	if got := strings.Join(res.Columns, ","); got != "大区,金额,日期" {
		t.Fatalf("列名 = %s", got)
	}
	if len(res.Preview) != 2 {
		t.Fatalf("预览行数 = %d", len(res.Preview))
	}
	// 日期列按确认后的 string 处理：值必须还是原始字符串。
	if v, _ := res.Preview[0].Values[2].(string); v != "2026-01-02" {
		t.Fatalf("日期列 = %#v，期望原样字符串", res.Preview[0].Values[2])
	}

	var schemaDoc struct {
		Columns []ColumnSchema `json:"columns"`
	}
	b, err := os.ReadFile(res.SchemaFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &schemaDoc); err != nil {
		t.Fatal(err)
	}
	if schemaDoc.Columns[2].Type != "string" || schemaDoc.Columns[2].Source != SourceConfirmed {
		t.Fatalf("Schema 未记录确认结果：%+v", schemaDoc.Columns[2])
	}
}

func TestConvertLayoutMismatch(t *testing.T) {
	dir := t.TempDir()
	path := writeSheet(t, dir, "t.xlsx", [][]any{{"a", "b"}, {1, 2}})
	l := &Layout{Format: DefaultConverterName, Version: LayoutVersion,
		Columns: []LayoutColumn{{Name: "a", Type: "integer"}}}
	out := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Convert(Options{Input: path, OutDir: out, Layout: l})
	if codeOf(err) != types.CodeUsage {
		t.Fatalf("列数不符应报 CodeUsage，实际 %v", err)
	}
	// 版本不符同样要拒绝，而不是当成 0 列结构继续跑。
	l2 := &Layout{Format: DefaultConverterName, Version: LayoutVersion + 1}
	if _, err := Convert(Options{Input: path, OutDir: out, Layout: l2}); codeOf(err) != types.CodeUsage {
		t.Fatalf("版本不符应报 CodeUsage，实际 %v", err)
	}
}

func TestLoadLayoutRejectsUnknownField(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"format":"xlsx","version":1,"headerrow":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLayout(bad); codeOf(err) != types.CodeUsage {
		t.Fatalf("拼错的字段名应报 CodeUsage，实际 %v", err)
	}
	good := filepath.Join(dir, "good.json")
	if err := os.WriteFile(good, []byte(`{"format":"xlsx","version":1,"header_row":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := LoadLayout(good)
	if err != nil || l.HeaderRow != 2 {
		t.Fatalf("LoadLayout 正常文件失败：%+v %v", l, err)
	}
}

func TestRehearseAlternatives(t *testing.T) {
	dir := t.TempDir()
	path := writeSheet(t, dir, "t.xlsx", [][]any{
		{"2026 年销售报表"},
		{"区域", "金额", "备注"},
		{"华东", 100, "重点"},
		{"华南", "N/A", "重点"}, // 金额列混进文本：应给出类型候选
		{"合计", 300, nil},
	})
	r, err := Rehearse(Options{Input: path})
	if err != nil {
		t.Fatal(err)
	}
	if r.Layout == nil || r.Layout.HeaderRow != 1 {
		t.Fatalf("默认方案应指出 header_row=1：%+v", r.Layout)
	}
	byCode := map[string][]Candidate{}
	for _, c := range r.Candidates {
		byCode[c.Code] = append(byCode[c.Code], c)
	}
	hdrs := byCode["header_row"]
	if len(hdrs) < 2 {
		t.Fatalf("应给出多个表头行候选，实际 %+v", hdrs)
	}
	var row1, row2 float64
	for _, c := range hdrs {
		switch c.Value {
		case "header_row=1":
			row1 = c.Score
		case "header_row=2":
			row2 = c.Score
		}
		if c.Effect == "" || c.Label == "" {
			t.Fatalf("候选必须带说明与影响：%+v", c)
		}
	}
	if !(row2 > row1) {
		t.Fatalf("第 2 行作为表头的打分应高于标题行：row1=%.2f row2=%.2f", row1, row2)
	}
	// 混合类型的列要有候选，且按占比降序；候选值可回填 Layout。
	types := byCode["type_ambiguity"]
	if len(types) < 2 {
		t.Fatalf("金额列混有文本，应给出类型候选，实际 %+v", types)
	}
	for i := 1; i < len(types); i++ {
		if types[i].Score > types[i-1].Score {
			t.Fatalf("候选应组内按打分降序：%+v", types)
		}
	}
}

func TestRehearseMultiSheet(t *testing.T) {
	dir := t.TempDir()
	f := excelize.NewFile()
	if err := f.SetSheetRow("Sheet1", "A1", &[]any{"区域", "金额"}); err != nil {
		t.Fatal(err)
	}
	if err := f.SetSheetRow("Sheet1", "A2", &[]any{"华东", 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.NewSheet("第二页"); err != nil {
		t.Fatal(err)
	}
	if err := f.SetSheetRow("第二页", "A1", &[]any{"产品", "数量"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "multi.xlsx")
	if err := f.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	r, err := Rehearse(Options{Input: path})
	if err != nil {
		t.Fatal(err)
	}
	var sheets []string
	for _, c := range r.Candidates {
		if c.Code == "sheet" {
			sheets = append(sheets, c.Value)
			if c.Score <= 0 || c.Score > 1 {
				t.Fatalf("工作表候选打分越界：%+v", c)
			}
		}
	}
	if len(sheets) != 2 {
		t.Fatalf("应给出两个工作表候选，实际 %v", sheets)
	}
}

func TestConvertLayoutRejectsLossyCoercion(t *testing.T) {
	dir := t.TempDir()
	path := writeSheet(t, dir, "t.xlsx", [][]any{
		{"区域", "金额"},
		{"华东", 100},
		{"华南", "N/A"}, // 整列因此不是 integer
	})
	l := &Layout{Format: DefaultConverterName, Version: LayoutVersion,
		Columns: []LayoutColumn{{Name: "区域", Type: "string"}, {Name: "金额", Type: "integer"}}}
	out := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Convert(Options{Input: path, OutDir: out, Layout: l})
	if codeOf(err) != types.CodeUsage || !strings.Contains(err.Error(), "金额") {
		t.Fatalf("把混文本的列强制成 integer 应报错并点名该列，实际 %v", err)
	}
	// 改成 string 就能通过，且该列取值为原始字符串。
	l.Columns[1].Type = "string"
	res, err := Convert(Options{Input: path, OutDir: out, Layout: l, PreviewRows: 5})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := res.Preview[1].Values[1].(string); v != "N/A" {
		t.Fatalf("金额列应保留原值，实际 %#v", res.Preview[1].Values[1])
	}
}

func TestAmbiguousZeroOneColumn(t *testing.T) {
	dir := t.TempDir()
	path := writeSheet(t, dir, "t.xlsx", [][]any{
		{"区域", "是否有效"},
		{"华东", 1},
		{"华南", 0},
		{"西南", 1},
	})
	l, err := Inspect(Options{Input: path})
	if err != nil {
		t.Fatal(err)
	}
	col := l.Columns[1]
	if col.Type != "integer" || len(col.Candidates) != 2 {
		t.Fatalf("0/1 列应给出 integer/boolean 两个候选，实际 %+v", col)
	}
	var asked bool
	for _, q := range l.Questions {
		if q.Code == "type_ambiguity" && q.Column == "是否有效" {
			asked = len(q.Options) == 2
		}
	}
	if !asked {
		t.Fatalf("0/1 列应提问（剩多个候选才问），实际问题 = %+v", l.Questions)
	}

	// 默认（不确认）：按 integer。
	out := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := Convert(Options{Input: path, OutDir: out, PreviewRows: 5})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := res.Preview[0].Values[1].(int64); v != 1 {
		t.Fatalf("默认应按 integer，实际 %#v", res.Preview[0].Values[1])
	}

	// 确认成 boolean：0/1 无损映射为 false/true。
	l.Columns[1].Type = "boolean"
	l.Columns[1].Source = SourceConfirmed
	res, err = Convert(Options{Input: path, OutDir: out, Layout: l, PreviewRows: 5})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := res.Preview[0].Values[1].(bool); !v {
		t.Fatalf("确认成 boolean 后 1 应变成 true，实际 %#v", res.Preview[0].Values[1])
	}
	if v, _ := res.Preview[1].Values[1].(bool); v {
		t.Fatalf("0 应变成 false，实际 %#v", res.Preview[1].Values[1])
	}
}

func TestInspectUsesExcelDateFormat(t *testing.T) {
	dir := t.TempDir()
	f := excelize.NewFile()
	style, err := f.NewStyle(&excelize.Style{NumFmt: 14}) // mm-dd-yy
	if err != nil {
		t.Fatal(err)
	}
	if err := f.SetSheetRow("Sheet1", "A1", &[]any{"区域", "日期"}); err != nil {
		t.Fatal(err)
	}
	for i, region := range []string{"华东", "华南"} {
		cell, _ := excelize.CoordinatesToCellName(1, i+2)
		if err := f.SetSheetRow("Sheet1", cell, &[]any{region, 46000 + i}); err != nil {
			t.Fatal(err)
		}
		dc, _ := excelize.CoordinatesToCellName(2, i+2)
		if err := f.SetCellStyle("Sheet1", dc, dc, style); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "date.xlsx")
	if err := f.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	l, err := Inspect(Options{Input: path})
	if err != nil {
		t.Fatal(err)
	}
	col := l.Columns[1]
	if col.Type != "date" {
		t.Fatalf("日期格式列应判成 date，实际 %+v", col)
	}
	if col.Evidence["excel_date"] == 0 {
		t.Fatalf("应记录 excelize 的日期格式证据（excel_date），实际 evidence=%v", col.Evidence)
	}
	if col.Format != "2006-01-02" && col.Format != "" {
		t.Fatalf("日期输出布局异常：%q", col.Format)
	}
}

func TestConfigureRejectsUnknownKey(t *testing.T) {
	dir := t.TempDir()
	path := writeSheet(t, dir, "t.xlsx", [][]any{{"a"}, {1}})
	out := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	// xlsx 目前没有可配置项：写了键就必须报错，且错误里要点名是哪个键。
	_, err := Convert(Options{Input: path, OutDir: out,
		ConverterOpts: map[string]any{"header_rows": 2, "encoding": "gbk"}})
	if codeOf(err) != types.CodeUsage {
		t.Fatalf("未知配置键应报 CodeUsage，实际 %v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "encoding") || !strings.Contains(msg, "header_rows") {
		t.Fatalf("错误应点名全部未知键，实际 %q", msg)
	}
}

func TestConfigureWrapsPlainError(t *testing.T) {
	// 转换器返回普通 error（非 *types.Error）时，要翻译成带配置路径提示的 CodeUsage。
	Register("failcfg", []string{".fc"}, func() Converter { return &failConfigConverter{} })
	defer unregister("failcfg")
	_, err := Convert(Options{Converter: "failcfg", Input: "x.fc"})
	te := types.AsError(err, types.CodeGeneral)
	if te.Code != types.CodeUsage || !strings.Contains(te.Hint, "convert.converter.failcfg") {
		t.Fatalf("应包装成 CodeUsage 并带上配置路径提示，实际 %+v", te)
	}
	// 已经是 *types.Error 的则原样透传（不重复包装）。
	Register("typedcfg", []string{".tc"}, func() Converter { return &typedErrorConverter{} })
	defer unregister("typedcfg")
	_, err = Convert(Options{Converter: "typedcfg", Input: "x.tc"})
	if te := types.AsError(err, types.CodeGeneral); te.Code != types.CodeExec || te.Hint != "keep" {
		t.Fatalf("typed error 应原样透传，实际 %+v", te)
	}
}

// failConfigConverter 让 Configure 返回普通 error。
type failConfigConverter struct{ stubConverter }

func (c *failConfigConverter) Name() string         { return "failcfg" }
func (c *failConfigConverter) Extensions() []string { return []string{".fc"} }
func (c *failConfigConverter) Configure(map[string]any) error {
	return errors.New("boom")
}

// typedErrorConverter 让 Configure 返回带错误码的 *types.Error。
type typedErrorConverter struct{ stubConverter }

func (c *typedErrorConverter) Name() string         { return "typedcfg" }
func (c *typedErrorConverter) Extensions() []string { return []string{".tc"} }
func (c *typedErrorConverter) Configure(map[string]any) error {
	return types.Errorf(types.CodeExec, "already typed").WithHint("keep")
}

func TestEntryErrorPaths(t *testing.T) {
	dir := t.TempDir()
	xlsxPath := writeSheet(t, dir, "t.xlsx", [][]any{{"a"}, {1}})
	out := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "nope.xlsx")

	cases := []struct {
		name string
		err  error
		code int
	}{
		{"Convert 文件不存在", func() error { _, err := Convert(Options{Input: missing, OutDir: out}); return err }(), types.CodeNotFound},
		{"Convert 扩展名不支持", func() error { _, err := Convert(Options{Input: "a.csv", OutDir: out}); return err }(), types.CodeUsage},
		{"Inspect 文件不存在", func() error { _, err := Inspect(Options{Input: missing}); return err }(), types.CodeNotFound},
		{"Inspect 扩展名不支持", func() error { _, err := Inspect(Options{Input: "a.csv"}); return err }(), types.CodeUsage},
		{"Rehearse 文件不存在", func() error { _, err := Rehearse(Options{Input: missing}); return err }(), types.CodeNotFound},
	}
	for _, c := range cases {
		if codeOf(c.err) != c.code {
			t.Fatalf("%s：期望 code %d，实际 %v", c.name, c.code, c.err)
		}
	}

	// 中断：ctx 已取消时三个入口都应返回 CodeInterrupted。
	ctx, cancel := contextWithCancel()
	cancel()
	if _, err := Convert(Options{Ctx: ctx, Input: xlsxPath, OutDir: out}); codeOf(err) != types.CodeInterrupted {
		t.Fatalf("Convert 应返回中断错误，实际 %v", err)
	}
	if _, err := Inspect(Options{Ctx: ctx, Input: xlsxPath}); codeOf(err) != types.CodeInterrupted {
		t.Fatalf("Inspect 应返回中断错误，实际 %v", err)
	}
	if _, err := Rehearse(Options{Ctx: ctx, Input: xlsxPath}); codeOf(err) != types.CodeInterrupted {
		t.Fatalf("Rehearse 应返回中断错误，实际 %v", err)
	}
}

func TestExcelFormatKind(t *testing.T) {
	f := excelize.NewFile()
	mk := func(st *excelize.Style) int {
		id, err := f.NewStyle(st)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	custom := func(s string) int { return mk(&excelize.Style{CustomNumFmt: &s}) }
	cases := []struct {
		name string
		id   int
		want string
	}{
		{"默认样式(0)", 0, ""},
		{"内置日期 14", mk(&excelize.Style{NumFmt: 14}), "date"},
		{"内置日期 22", mk(&excelize.Style{NumFmt: 22}), "date"},
		{"内置百分比 9", mk(&excelize.Style{NumFmt: 9}), "percent"},
		// 内置货币（NumFmt 5~8）用 NewStyle 建不出来（excelize 会退回默认样式），
		// 真实文件里由 Excel 写入 numFmtId=7 时会走到同一分支，这里用自定义格式覆盖。
		{"自定义货币 ¥", custom("¥#,##0.00"), "currency"},
		{"自定义货币 $", custom("$#,##0.00"), "currency"},
		{"自定义日期", custom("yyyy年m月d日"), "date"},
		{"自定义百分比", custom("0.00%"), "percent"},
		{"自定义普通数字", custom("0.00"), ""},
		{"自定义含字面年月", custom(`"年"0"月"`), ""},
	}
	for _, c := range cases {
		if got := excelFormatKind(f, c.id); got != c.want {
			t.Fatalf("%s：期望 %q，实际 %q", c.name, c.want, got)
		}
	}
}

// contextWithCancel 让用例读起来短一点（只取 CancelFunc）。
func contextWithCancel() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

func TestNormalizeExtAndRehearseError(t *testing.T) {
	cases := map[string]string{
		"":        "",
		"  ":      "",
		"xlsx":    ".xlsx",
		" .XLSX ": ".xlsx",
		".csv":    ".csv",
	}
	for in, want := range cases {
		if got := normalizeExt(in); got != want {
			t.Fatalf("normalizeExt(%q) = %q，期望 %q", in, got, want)
		}
	}
	// 包级 Rehearse 也要走一遍解析/配置失败路径。
	if _, err := Rehearse(Options{Converter: "nope", Input: "a.xlsx"}); codeOf(err) != types.CodeUsage {
		t.Fatalf("未知转换器应报 CodeUsage，实际 %v", err)
	}
}

func TestRehearseHidesCellValuesByDefault(t *testing.T) {
	dir := t.TempDir()
	// 用明显的「数据值」做第 2 行：默认（强隐私）标签里不应出现它们。
	path := writeSheet(t, dir, "t.xlsx", [][]any{
		{"2026 年销售报表"},
		{"华东仓-机密", "机密金额"},
		{"华南", 200},
	})
	for _, samples := range []bool{false, true} {
		r, err := Rehearse(Options{Input: path, IncludeSamples: samples})
		if err != nil {
			t.Fatal(err)
		}
		var labels []string
		for _, c := range r.Candidates {
			if c.Code == "header_row" {
				labels = append(labels, c.Label)
			}
		}
		joined := strings.Join(labels, " | ")
		leaked := strings.Contains(joined, "机密")
		if leaked != samples {
			t.Fatalf("IncludeSamples=%v 时泄漏状态不对：%s", samples, joined)
		}
		if !strings.Contains(joined, "第 2 行") {
			t.Fatalf("仍应报告表头行号，实际 %s", joined)
		}
	}
}
