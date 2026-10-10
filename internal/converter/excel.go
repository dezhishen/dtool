package converter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dezhishen/dtool/internal/workspace"
	"github.com/dezhishen/dtool/pkg/types"
	"github.com/xuri/excelize/v2"
)

type Options struct {
	// Ctx 用于响应中断（Ctrl+C / SIGTERM）；nil 表示不检查。
	Ctx         context.Context
	Input       string
	Sheet       string
	OutDir      string
	UpdatedAt   time.Time // 写入 Schema 的更新日期，零值取当前时间
	PreviewRows int       // 保留多少条类型化记录供调用方预览（0 表示不留）
}

type Result struct {
	DataFile    string
	SchemaFile  string
	Sheet       string
	Columns     []string
	RecordCount int
	Preview     []types.Row // 前 PreviewRows 条记录（类型化），供 Action 预览
	Warnings    []string
}

// ConvertExcel 流式读取 xlsx，写出 data.json（保持列顺序）与 data.schema.json。
//
// 两阶段、单次解析：
//
//	阶段 1：逐行读单元格 → 累积每列类型统计，并把原始字符串按行追加到临时 JSONL；
//	阶段 2：依据统计出的 Schema 读回 JSONL，逐行写成最终 data.json。
//
// 全程只驻留「一行 + 每列统计」，峰值内存与行数无关。旧实现把全部单元格、
// 转置副本、类型化行以及整块序列化的 JSON 同时放在内存里（150k 行实测 1.5GB）。
func ConvertExcel(o Options) (*Result, error) {
	ctx := o.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, interruptedErr()
	}
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
	defer func() { _ = f.Close() }() // 兜底；下面会更早关闭一次以释放临时文件

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

	if o.PreviewRows == 0 {
		o.PreviewRows = 20 // 与 action.Preview 的默认值保持一致
	}

	res := &Result{Sheet: sheet}
	// 合并单元格数量：流式扫描（excelize 的 GetMergeCells 会整表物化）。
	// 失败时静默跳过——这只是个提示性告警，不值得为它付出内存代价。
	if n, err := mergeCount(o.Input, sheet); err == nil && n > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("检测到 %d 个合并单元格，仅左上角有值，其余为 null", n))
	}

	// 阶段 1：扫描单元格 → 列统计 + 临时 JSONL
	rowsFile := filepath.Join(o.OutDir, ".rows.jsonl.tmp")
	out, err := os.Create(rowsFile)
	if err != nil {
		return nil, err
	}
	headers, stats, records, scanWarns, err := scanRows(ctx, f, sheet, out)
	if err != nil {
		out.Close()
		os.Remove(rowsFile)
		return nil, err
	}
	if err := out.Close(); err != nil {
		os.Remove(rowsFile)
		return nil, err
	}
	_ = f.Close() // excelize 会把大表解压到临时文件，尽早释放
	defer os.Remove(rowsFile)

	if headers == nil {
		return nil, fmt.Errorf("sheet %q is empty", sheet)
	}
	res.Columns = headers
	res.RecordCount = records
	res.Warnings = append(res.Warnings, scanWarns...)

	schema := make([]ColumnSchema, len(headers))
	for i, h := range headers {
		schema[i] = stats[i].column(h, records)
	}

	// 阶段 2：按 Schema 把 JSONL 写成最终 data.json
	res.DataFile = filepath.Join(o.OutDir, "data.json")
	if err := writeTypedRows(ctx, rowsFile, res.DataFile, headers, schema, o.PreviewRows, &res.Preview); err != nil {
		return nil, err
	}

	updated := o.UpdatedAt
	if updated.IsZero() {
		updated = time.Now()
	}
	updated = updated.UTC()
	res.SchemaFile = filepath.Join(o.OutDir, "data.schema.json")
	doc := map[string]any{
		"source":       filepath.Base(o.Input),
		"sheet":        sheet,
		"record_count": records,
		"updated_at":   updated.Format(time.RFC3339),
		"columns":      schema,
	}
	if err := workspace.WriteJSONAtomic(res.SchemaFile, doc); err != nil {
		return nil, err
	}
	return res, nil
}

// scanRows 逐行读取工作表：首行作为表头，之后每行累积列统计并写入 JSONL。
// 返回表头、每列统计与数据行数。
// rowsCheckInterval 是转换过程中检查中断的间隔（行）。转换是流式的，检查开销可忽略。
const rowsCheckInterval = 4096

// interruptedErr 把「转换被信号打断」表达成可读错误：该步未完成、结果未知，重跑即可。
func interruptedErr() error {
	return types.Errorf(types.CodeInterrupted, "已中断：Excel 转换未完成（收到 Ctrl+C / SIGTERM）").
		WithHint("重跑该命令即可；上一次的临时文件已由 staging 目录清理")
}

func scanRows(ctx context.Context, f *excelize.File, sheet string, w io.Writer) ([]string, []*colStat, int, []string, error) {
	it, err := f.Rows(sheet)
	if err != nil {
		return nil, nil, 0, nil, err
	}
	defer it.Close()

	var (
		headers  []string
		stats    []*colStat
		records  int
		warnings []string
	)
	bw := bufio.NewWriterSize(w, 1<<20)
	defer bw.Flush()
	for rows := 0; it.Next(); {
		if rows%rowsCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return nil, nil, 0, nil, interruptedErr()
			}
		}
		cells, err := it.Columns()
		if err != nil {
			return nil, nil, 0, nil, err
		}
		if allBlank(cells) {
			continue
		}
		if headers == nil {
			headers, warnings = normalizeHeaders(cells, warnings)
			stats = make([]*colStat, len(headers))
			for i := range stats {
				stats[i] = newColStat()
			}
			continue
		}
		rows++
		line := make([]string, len(headers))
		for i := range headers {
			if i < len(cells) {
				line[i] = cells[i]
			}
			stats[i].observe(line[i])
		}
		b, err := json.Marshal(line)
		if err != nil {
			return nil, nil, 0, nil, err
		}
		if _, err := bw.Write(b); err != nil {
			return nil, nil, 0, nil, err
		}
		if err := bw.WriteByte('\n'); err != nil {
			return nil, nil, 0, nil, err
		}
		records++
	}
	if err := it.Error(); err != nil {
		return nil, nil, 0, nil, err
	}
	return headers, stats, records, warnings, nil
}

// writeTypedRows 读回 JSONL，按 Schema 把每行类型化后写成 data.json；
// 需要预览时保留前 preview 条（类型化记录）。
func writeTypedRows(ctx context.Context, rowsFile, dataFile string, headers []string, schema []ColumnSchema, preview int, out *[]types.Row) error {
	in, err := os.Open(rowsFile)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dataFile), ".data-*.json")
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(tmp, 1<<20)
	br := bufio.NewReaderSize(in, 1<<20)
	rw := &rowWriter{w: bw}
	for n := 0; ; n++ {
		if n%rowsCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				_ = tmp.Close()
				_ = os.Remove(tmp.Name())
				return interruptedErr()
			}
		}
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var cells []string
			if uerr := json.Unmarshal(bytes.TrimRight(line, "\n"), &cells); uerr != nil {
				tmp.Close()
				os.Remove(tmp.Name())
				return uerr
			}
			vals := make([]any, len(headers))
			for i := range headers {
				v := ""
				if i < len(cells) {
					v = cells[i]
				}
				vals[i] = convertCell(schema[i].Type, v)
			}
			row := types.Row{Columns: headers, Values: vals}
			if err := rw.Write(row); err != nil {
				tmp.Close()
				os.Remove(tmp.Name())
				return err
			}
			if preview > 0 && len(*out) < preview {
				*out = append(*out, row)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return err
		}
	}
	if err := rw.Close(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := bw.Flush(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), dataFile)
}

// rowWriter 把行流式写成与 json.MarshalIndent([]types.Row, "", "  ") 完全一致的字节：
// 数组元素缩进 2 空格，元素内成员再缩进 2 空格，结尾补一个换行（与 WriteJSONAtomic 一致）。
type rowWriter struct {
	w   *bufio.Writer
	n   int
	row bytes.Buffer
	ind bytes.Buffer
}

func (rw *rowWriter) Write(r types.Row) error {
	b, err := r.MarshalJSON()
	if err != nil {
		return err
	}
	rw.row.Reset()
	if rw.n == 0 {
		rw.row.WriteString("[\n")
	} else {
		rw.row.WriteString(",\n")
	}
	rw.row.WriteString("  ")
	rw.ind.Reset()
	if err := json.Indent(&rw.ind, b, "  ", "  "); err != nil {
		return err
	}
	rw.row.Write(rw.ind.Bytes())
	rw.n++
	_, err = rw.w.Write(rw.row.Bytes())
	return err
}

func (rw *rowWriter) Close() error {
	if rw.n == 0 {
		_, err := rw.w.WriteString("[]\n")
		return err
	}
	_, err := rw.w.WriteString("\n]\n")
	return err
}

func allBlank(cells []string) bool {
	for _, c := range cells {
		if !isBlank(c) {
			return false
		}
	}
	return true
}

// normalizeHeaders 把空表头命名为 col_N、重复表头追加 _N，并记录告警。
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
