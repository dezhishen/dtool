package formatter

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dezhishen/dtool/pkg/types"
	"github.com/xuri/excelize/v2"
)

func sample() *types.QueryResult {
	cols := []string{"region", "total", "note"}
	return &types.QueryResult{Success: true, Columns: cols, RowCount: 2, Rows: []types.Row{
		{Columns: cols, Values: []any{"North", int64(150), "a|b"}},
		{Columns: cols, Values: []any{"南", 250.5, nil}},
	}}
}

func render(t *testing.T, format string) string {
	t.Helper()
	var sb strings.Builder
	if err := Write(&sb, format, sample()); err != nil {
		t.Fatal(err)
	}
	return sb.String()
}

func TestCSV(t *testing.T) {
	want := "region,total,note\nNorth,150,a|b\n南,250.5,\n"
	if got := render(t, "csv"); got != want {
		t.Fatalf("got %q", got)
	}
}

func TestMarkdownEscapesPipes(t *testing.T) {
	got := render(t, "markdown")
	want := "| region | total | note |\n| --- | --- | --- |\n| North | 150 | a\\|b |\n| 南 | 250.5 |  |\n"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestJSONKeepsOrderAndCJK(t *testing.T) {
	got := render(t, "json")
	i, j, k := strings.Index(got, `"region"`), strings.Index(got, `"total"`), strings.Index(got, `"note"`)
	if !(i < j && j < k) || !strings.Contains(got, "南") {
		t.Fatalf("got %s", got)
	}
	var back map[string]any
	if err := json.Unmarshal([]byte(got), &back); err != nil || back["row_count"].(float64) != 2 {
		t.Fatalf("invalid JSON: %v", err)
	}
}

func TestTable(t *testing.T) {
	got := render(t, "table")
	for _, s := range []string{"region", "North", "250.5"} {
		if !strings.Contains(got, s) {
			t.Fatalf("missing %q in %s", s, got)
		}
	}
}

func TestUnsupportedFormat(t *testing.T) {
	err := Write(&strings.Builder{}, "xml", sample())
	te, ok := err.(*types.Error)
	if !ok || te.Code != types.CodeUsage {
		t.Fatalf("err = %v", err)
	}
}

func TestCell(t *testing.T) {
	cases := map[string]any{"": nil, "1": json.Number("1"), "2.5": 2.5, "3": 3.0, "x": "x", "true": true}
	for want, in := range cases {
		if got := Cell(in); got != want {
			t.Errorf("Cell(%#v) = %q, want %q", in, got, want)
		}
	}
}

func TestXLSX(t *testing.T) {
	cols := []string{"区域", "total", "note"}
	r := &types.QueryResult{Columns: cols, RowCount: 3, Rows: []types.Row{
		{Columns: cols, Values: []any{"北", int64(150), "a"}},
		{Columns: cols, Values: []any{"南", 250.5, nil}},
		{Columns: cols, Values: []any{"东", json.Number("7"), "长文本长文本长文本长文本"}},
	}}
	var buf bytes.Buffer
	if err := Write(&buf, "xlsx", r); err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("not a valid xlsx: %v", err)
	}
	defer f.Close()

	rows, _ := f.GetRows("Sheet1")
	if len(rows) != 4 || rows[0][0] != "区域" || rows[0][1] != "total" {
		t.Fatalf("rows = %v", rows)
	}
	if rows[1][1] != "150" || rows[2][1] != "250.5" || rows[3][1] != "7" {
		t.Fatalf("numbers = %v", rows)
	}
	// 数值写成数字单元格而非文本，Excel 里才能求和/排序
	for _, cell := range []string{"B2", "B3", "B4"} {
		if typ, _ := f.GetCellType("Sheet1", cell); typ == excelize.CellTypeSharedString || typ == excelize.CellTypeInlineString {
			t.Errorf("%s stored as text", cell)
		}
	}
	if len(rows[2]) > 2 && rows[2][2] != "" {
		t.Errorf("null should be an empty cell: %q", rows[2][2])
	}
	if w, _ := f.GetColWidth("Sheet1", "C"); w <= 8 {
		t.Errorf("column width not adjusted: %v", w)
	}
}

func TestXLSXMetadataIsDtoolNotLibraryDefaults(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, "xlsx", sample()); err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	doc, _ := f.GetDocProps()
	app, _ := f.GetAppProps()
	if doc.Creator != "dtool" || doc.LastModifiedBy != "dtool" || app.Application != "dtool" {
		t.Fatalf("doc=%+v app=%+v", doc, app)
	}
	if strings.HasPrefix(doc.Created, "2006") || strings.HasPrefix(doc.Modified, "2006") {
		t.Fatalf("library default timestamps leaked: %+v", doc)
	}
}

func TestXLSXEmptyResult(t *testing.T) {
	r := &types.QueryResult{Columns: []string{"a", "b"}, Rows: []types.Row{}}
	var buf bytes.Buffer
	if err := Write(&buf, "xlsx", r); err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if rows, _ := f.GetRows("Sheet1"); len(rows) != 1 {
		t.Fatalf("rows = %v", rows)
	}
}
