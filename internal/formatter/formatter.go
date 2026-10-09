package formatter

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/dezhishen/dtool/pkg/types"
	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/tw"
)

var Formats = []string{"json", "csv", "markdown", "table", "xlsx"}

// Cell 把单元格值转为文本。
func Cell(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case json.Number:
		return x.String()
	}
	return fmt.Sprint(v)
}

func records(r *types.QueryResult) [][]string {
	out := make([][]string, len(r.Rows))
	for i, row := range r.Rows {
		rec := make([]string, len(r.Columns))
		for j, c := range r.Columns {
			if v, ok := row.Get(c); ok {
				rec[j] = Cell(v)
			}
		}
		out[i] = rec
	}
	return out
}

// Write 按 format 输出 json / csv / markdown / table / xlsx（xlsx 为二进制，调用方应写文件）。
func Write(w io.Writer, format string, r *types.QueryResult) error {
	switch format {
	case "", "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(r)
	case "csv":
		cw := csv.NewWriter(w)
		if err := cw.Write(r.Columns); err != nil {
			return err
		}
		if err := cw.WriteAll(records(r)); err != nil {
			return err
		}
		cw.Flush()
		return cw.Error()
	case "markdown":
		esc := func(s string) string {
			return strings.ReplaceAll(strings.ReplaceAll(s, "|", `\|`), "\n", " ")
		}
		line := func(cells []string) string {
			p := make([]string, len(cells))
			for i, c := range cells {
				p[i] = esc(c)
			}
			return "| " + strings.Join(p, " | ") + " |\n"
		}
		sep := make([]string, len(r.Columns))
		for i := range sep {
			sep[i] = "---"
		}
		var sb strings.Builder
		sb.WriteString(line(r.Columns))
		sb.WriteString(line(sep))
		for _, rec := range records(r) {
			sb.WriteString(line(rec))
		}
		_, err := io.WriteString(w, sb.String())
		return err
	case "xlsx":
		return writeXLSX(w, r)
	case "table":
		t := tablewriter.NewTable(w, tablewriter.WithHeaderAutoFormat(tw.Off)) // 保持列名原样，不转大写
		hdr := make([]any, len(r.Columns))
		for i, c := range r.Columns {
			hdr[i] = c
		}
		t.Header(hdr...)
		if err := t.Bulk(records(r)); err != nil {
			return err
		}
		return t.Render()
	}
	return types.Errorf(types.CodeUsage, "unsupported format %q", format).
		WithHint("可选: " + strings.Join(Formats, " / "))
}
