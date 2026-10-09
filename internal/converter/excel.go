package converter

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dezhishen/dtool/internal/workspace"
	"github.com/dezhishen/dtool/pkg/types"
	"github.com/xuri/excelize/v2"
)

type Options struct {
	Input     string
	Sheet     string
	OutDir    string
	UpdatedAt time.Time // 写入 Schema 的更新日期，零值取当前时间
}

type Result struct {
	DataFile   string
	SchemaFile string
	Sheet      string
	Columns    []string
	Rows       []types.Row
	Warnings   []string
}

// ConvertExcel 流式读取 xlsx，整列推断类型后写出 data.json（保持列顺序）与 data.schema.json。
func ConvertExcel(o Options) (*Result, error) {
	if _, err := os.Stat(o.Input); err != nil {
		return nil, types.Errorf(types.CodeNotFound, "file not found: %s", o.Input)
	}
	if !strings.EqualFold(filepath.Ext(o.Input), ".xlsx") {
		return nil, types.Errorf(types.CodeUsage, "unsupported file type %q", filepath.Ext(o.Input)).
			WithHint("仅支持 .xlsx")
	}
	f, err := excelize.OpenFile(o.Input)
	if err != nil {
		return nil, fmt.Errorf("open excel: %w", err)
	}
	defer f.Close()

	sheets := f.GetSheetList()
	sheet := o.Sheet
	if sheet == "" {
		if len(sheets) == 0 {
			return nil, fmt.Errorf("workbook has no sheets")
		}
		sheet = sheets[0]
	} else if idx, _ := f.GetSheetIndex(sheet); idx < 0 {
		return nil, types.Errorf(types.CodeNotFound, "sheet not found: %s", sheet).
			WithDetail("available sheets: " + strings.Join(sheets, ", "))
	}

	res := &Result{Sheet: sheet}
	if merged, _ := f.GetMergeCells(sheet); len(merged) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("检测到 %d 个合并单元格，仅左上角有值，其余为 null", len(merged)))
	}

	it, err := f.Rows(sheet)
	if err != nil {
		return nil, err
	}
	defer it.Close()

	var headers []string
	var rows [][]string
	for it.Next() {
		cells, err := it.Columns()
		if err != nil {
			return nil, err
		}
		if allBlank(cells) {
			continue
		}
		if headers == nil {
			headers, res.Warnings = normalizeHeaders(cells, res.Warnings)
			continue
		}
		rows = append(rows, cells)
	}
	if headers == nil {
		return nil, fmt.Errorf("sheet %q is empty", sheet)
	}
	res.Columns = headers

	cols := make([][]string, len(headers))
	for i := range cols {
		cols[i] = make([]string, len(rows))
	}
	for r, row := range rows {
		for c := range headers {
			if c < len(row) {
				cols[c][r] = row[c]
			}
		}
	}
	schema := InferSchema(headers, cols, len(rows))

	res.Rows = make([]types.Row, len(rows))
	for r := range rows {
		vals := make([]any, len(headers))
		for c := range headers {
			vals[c] = convertCell(schema[c].Type, cols[c][r])
		}
		res.Rows[r] = types.Row{Columns: headers, Values: vals}
	}

	updated := o.UpdatedAt
	if updated.IsZero() {
		updated = time.Now()
	}
	updated = updated.UTC()
	res.DataFile = filepath.Join(o.OutDir, "data.json")
	if err := writeJSON(res.DataFile, res.Rows); err != nil {
		return nil, err
	}
	res.SchemaFile = filepath.Join(o.OutDir, "data.schema.json")
	doc := map[string]any{
		"source":       filepath.Base(o.Input),
		"sheet":        sheet,
		"record_count": len(rows),
		"updated_at":   updated.Format(time.RFC3339),
		"columns":      schema,
	}
	if err := workspace.WriteJSONAtomic(res.SchemaFile, doc); err != nil {
		return nil, err
	}
	return res, nil
}

func writeJSON(path string, rows []types.Row) error {
	if rows == nil {
		rows = []types.Row{}
	}
	return workspace.WriteJSONAtomic(path, rows)
}

func allBlank(cells []string) bool {
	for _, c := range cells {
		if !isBlank(c) {
			return false
		}
	}
	return true
}

// normalizeHeaders 为空表头补 col_N，重复表头追加 _N，保证 JSON key 唯一。
func normalizeHeaders(raw []string, warns []string) ([]string, []string) {
	out := make([]string, len(raw))
	used := map[string]int{}
	for i, h := range raw {
		name := strings.TrimSpace(h)
		if name == "" {
			name = fmt.Sprintf("col_%d", i+1)
			warns = append(warns, fmt.Sprintf("第 %d 列表头为空，已命名为 %s", i+1, name))
		}
		base := name
		for {
			used[name]++
			if used[name] == 1 {
				break
			}
			name = fmt.Sprintf("%s_%d", base, used[base])
		}
		if name != strings.TrimSpace(h) && strings.TrimSpace(h) != "" {
			warns = append(warns, fmt.Sprintf("表头 %q 重复，已重命名为 %s", h, name))
		}
		out[i] = name
	}
	return out, warns
}
