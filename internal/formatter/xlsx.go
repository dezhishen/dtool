package formatter

import (
	"encoding/json"
	"io"
	"strconv"
	"unicode"

	"github.com/dezhishen/dtool/pkg/types"
	"github.com/xuri/excelize/v2"
)

const (
	xlsxSheet    = "Sheet1"
	minColWidth  = 8
	maxColWidth  = 60
	xlsxCellMax  = 32767
	xlsxRowLimit = 1048575 // 含表头共 1048576 行
)

// xlsxValue 保持数值为数字单元格，json.Number 按整数/浮点还原。
func xlsxValue(v any) any {
	switch x := v.(type) {
	case json.Number:
		if n, err := strconv.ParseInt(string(x), 10, 64); err == nil {
			return n
		}
		if f, err := x.Float64(); err == nil {
			return f
		}
		return string(x)
	case string:
		if r := []rune(x); len(r) > xlsxCellMax {
			return string(r[:xlsxCellMax])
		}
	}
	return v
}

func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || r > 0x2E80 {
			w += 2
		} else {
			w++
		}
	}
	return w
}

func writeXLSX(w io.Writer, r *types.QueryResult) error {
	if len(r.Rows) > xlsxRowLimit {
		return types.Errorf(types.CodeExec, "too many rows for xlsx (%d > %d)", len(r.Rows), xlsxRowLimit).
			WithHint("改用 csv，或在 SQL 中加 LIMIT")
	}
	f := excelize.NewFile()
	defer f.Close()

	hdr := make([]any, len(r.Columns))
	widths := make([]int, len(r.Columns))
	for i, c := range r.Columns {
		hdr[i] = c
		widths[i] = displayWidth(c)
	}
	if len(hdr) > 0 {
		if err := f.SetSheetRow(xlsxSheet, "A1", &hdr); err != nil {
			return err
		}
		if bold, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}}); err == nil {
			last, _ := excelize.CoordinatesToCellName(len(hdr), 1)
			_ = f.SetCellStyle(xlsxSheet, "A1", last, bold)
		}
	}
	for ri, row := range r.Rows {
		vals := make([]any, len(r.Columns))
		for ci, c := range r.Columns {
			v, _ := row.Get(c)
			vals[ci] = xlsxValue(v)
			if wd := displayWidth(Cell(vals[ci])); wd > widths[ci] {
				widths[ci] = wd
			}
		}
		cell, _ := excelize.CoordinatesToCellName(1, ri+2)
		if err := f.SetSheetRow(xlsxSheet, cell, &vals); err != nil {
			return err
		}
	}
	for i, wd := range widths {
		wd = min(max(wd+2, minColWidth), maxColWidth)
		name, _ := excelize.ColumnNumberToName(i + 1)
		_ = f.SetColWidth(xlsxSheet, name, name, float64(wd))
	}
	return f.Write(w)
}
