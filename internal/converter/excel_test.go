package converter

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dezhishen/dtool/pkg/types"
	"github.com/xuri/excelize/v2"
)

// writeTrickyXlsx 生成一个覆盖各种边界的工作表：空表头、重复表头、空行、短行、
// 前导零、超长整数、布尔、日期、特殊字符、合并单元格。
func writeTrickyXlsx(t *testing.T, dir string) string {
	t.Helper()
	f := excelize.NewFile()
	rows := [][]any{
		{"名称", "数量", "比率", "开关", "日期", "编号", "备注"},
		{"甲", 1, 1.5, true, "2026-01-02", "00123", "<b>&x</b>"},
		{"乙", -20, 0.25, false, "2026/1/2", 456, "含,逗号"},
		{nil, nil, nil, nil, nil, nil, nil}, // 整行空白：应被跳过
		{"丙", 12345678901234567, 0.5, "TRUE", "2026-01-02T03:04:05", 9, `引号"与\n`},
		{"丁", 3, 3e2, "yes", "不是日期", nil, nil}, // 短行：缺失单元格按空处理
	}
	for i, r := range rows {
		cell, err := excelize.CoordinatesToCellName(1, i+1)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.SetSheetRow("Sheet1", cell, &r); err != nil {
			t.Fatal(err)
		}
	}
	// 合并单元格（只影响告警，不影响数据）
	if err := f.MergeCell("Sheet1", "A1", "B1"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "tricky.xlsx")
	if err := f.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	return path
}

// referenceConvert 用改造前的算法（整表载入 → 转置 → InferSchema → 类型化行）算出
// 期望结果，作为流式实现的对照。
func referenceConvert(t *testing.T, path string) (headers []string, schema []ColumnSchema, rows []types.Row, warnings []string) {
	t.Helper()
	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	it, err := f.Rows(f.GetSheetList()[0])
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	var raw [][]string
	for it.Next() {
		cells, err := it.Columns()
		if err != nil {
			t.Fatal(err)
		}
		if allBlank(cells) {
			continue
		}
		if headers == nil {
			headers, warnings = normalizeHeaders(cells, warnings)
			continue
		}
		raw = append(raw, cells)
	}
	records := len(raw)
	cols := make([][]string, len(headers))
	for i := range cols {
		cols[i] = make([]string, records)
	}
	for r, row := range raw {
		for i := range headers {
			if i < len(row) {
				cols[i][r] = row[i]
			}
		}
	}
	schema = InferSchema(headers, cols, records)
	rows = make([]types.Row, records)
	for r := range raw {
		vals := make([]any, len(headers))
		for i := range headers {
			vals[i] = convertCell(schema[i].Type, cols[i][r])
		}
		rows[r] = types.Row{Columns: headers, Values: vals}
	}
	return headers, schema, rows, warnings
}

// TestConvertExcelMatchesReference 流式实现必须与改造前的算法产出完全相同的
// 表头、Schema 与 data.json 字节（含缩进格式）。
func TestConvertExcelMatchesReference(t *testing.T) {
	dir := t.TempDir()
	path := writeTrickyXlsx(t, dir)

	wantHeaders, wantSchema, wantRows, wantWarns := referenceConvert(t, path)
	res, err := ConvertExcel(Options{Input: path, OutDir: dir, PreviewRows: 2})
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(res.Columns, wantHeaders) {
		t.Fatalf("columns:\n got %q\nwant %q", res.Columns, wantHeaders)
	}
	if res.RecordCount != len(wantRows) {
		t.Fatalf("record count = %d, want %d", res.RecordCount, len(wantRows))
	}
	got, err := os.ReadFile(res.DataFile)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.MarshalIndent(wantRows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, append(want, '\n')) {
		t.Fatalf("data.json 与 MarshalIndent 不一致：\n got %s\nwant %s", got, append(want, '\n'))
	}
	if len(res.Preview) != 2 || !reflect.DeepEqual(res.Preview, wantRows[:2]) {
		t.Fatalf("preview = %#v", res.Preview)
	}

	// Schema：与参照实现逐字段一致（updated_at 除外）
	raw, err := os.ReadFile(res.SchemaFile)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["record_count"].(float64) != float64(len(wantRows)) || doc["source"] != "tricky.xlsx" {
		t.Fatalf("schema doc = %v", doc)
	}
	colsJSON, err := json.Marshal(doc["columns"])
	if err != nil {
		t.Fatal(err)
	}
	var gotSchema []ColumnSchema
	if err := json.Unmarshal(colsJSON, &gotSchema); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotSchema, wantSchema) {
		t.Fatalf("schema columns:\n got %+v\nwant %+v", gotSchema, wantSchema)
	}
	for _, w := range wantWarns {
		if !contains(res.Warnings, w) {
			t.Errorf("warnings %q 缺少 %q", res.Warnings, w)
		}
	}
	if !contains(res.Warnings, "合并单元格") {
		t.Errorf("warnings %q 缺少合并单元格告警", res.Warnings)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

// TestConvertExcelEmptyData 只有表头（或整行为空）时 data.json 应为 []，与
// json.MarshalIndent(空切片) 一致。
func TestConvertExcelEmptyData(t *testing.T) {
	dir := t.TempDir()
	f := excelize.NewFile()
	if err := f.SetSheetRow("Sheet1", "A1", &[]any{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if err := f.SetSheetRow("Sheet1", "A2", &[]any{nil, nil}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "empty.xlsx")
	if err := f.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	res, err := ConvertExcel(Options{Input: path, OutDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(res.DataFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "[]\n" {
		t.Fatalf("data.json = %q, want %q", got, "[]\\n")
	}
	if res.RecordCount != 0 || len(res.Preview) != 0 {
		t.Fatalf("res = %+v", res)
	}
}

// TestMergeCount 流式扫描合并单元格，结果与 excelize 的 GetMergeCells 一致。
func TestMergeCount(t *testing.T) {
	dir := t.TempDir()
	path := writeTrickyXlsx(t, dir) // 含 1 处合并
	n, err := mergeCount(path, "Sheet1")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("mergeCount = %d, want 1", n)
	}
	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	want, err := f.GetMergeCells("Sheet1")
	if err != nil {
		t.Fatal(err)
	}
	if n != len(want) {
		t.Fatalf("mergeCount = %d, excelize = %d", n, len(want))
	}
	// 无合并的表
	plain := filepath.Join(dir, "plain.xlsx")
	pf := excelize.NewFile()
	if err := pf.SetSheetRow("Sheet1", "A1", &[]any{"a"}); err != nil {
		t.Fatal(err)
	}
	if err := pf.SaveAs(plain); err != nil {
		t.Fatal(err)
	}
	if n, err := mergeCount(plain, "Sheet1"); err != nil || n != 0 {
		t.Fatalf("plain mergeCount = %d, %v", n, err)
	}
}
