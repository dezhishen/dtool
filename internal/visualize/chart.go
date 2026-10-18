package visualize

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/dezhishen/dtool/internal/formatter"
	"github.com/dezhishen/dtool/pkg/types"
	charts "github.com/vicanso/go-charts/v2"
)

const (
	maxPoints = 1000
	// maxSeries 限制 --group-by 透出的系列数：再多图例与配色都会失去可读性。
	maxSeries = 50
)

type Options struct {
	Type string // bar / line / pie / table
	X    string
	// Y 是数值列：一个列单系列；多个列各成一个系列（宽表直取）。
	Y []string
	// GroupBy 非空时按该字段拆分多系列（仅 bar / line）：每个系列是一条线或一组柱子。
	GroupBy string
	// Missing 决定 group-by 缺失组合的处理：zero（默认，补 0）/ gap（置空，line 断线）。
	Missing  string
	Title    string
	Format   string   // png / svg
	FontPath string   // 可选 TTF，用于显示中文
	OutFile  string   // 输出文件（绝对路径，扩展名由调用方根据 Ext 决定）
	FontDirs []string // 系统字体搜索目录，nil 表示使用平台默认
}

type Result struct {
	Points int
	// Series 是 --group-by 的系列名，按图例顺序；单系列时为空。
	Series []string
	// Missing 是实际使用的缺失处理（空表示 zero）；gap 时 line 会断线。
	Missing  string
	Font     string // 实际使用的字体文件，空表示内置默认字体
	Warnings []string
}

// Ext 返回输出文件扩展名。
func Ext(chartType, format string) string {
	if chartType == "table" {
		if format == "xlsx" {
			return "xlsx"
		}
		return "md"
	}
	if format == "svg" {
		return "svg"
	}
	return "png"
}

// LoadRows 读取 JSON 数组文件，返回列（按首次出现顺序）与行。
func LoadRows(path string) ([]string, []types.Row, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var rows []types.Row
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, nil, types.Errorf(types.CodeExec, "%s is not a JSON array of objects: %v", path, err)
	}
	seen := map[string]bool{}
	var cols []string
	for _, r := range rows {
		for _, c := range r.Columns {
			if !seen[c] {
				seen[c] = true
				cols = append(cols, c)
			}
		}
	}
	return cols, rows, nil
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case float64:
		return x, true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

// ParseY 把 --y 的逗号分隔列名拆开：去空白、忽略空项，保留书写顺序。
func ParseY(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Render 校验字段并渲染图表/表格到 o.OutFile。
func Render(cols []string, rows []types.Row, o Options) (*Result, error) {
	res := &Result{Points: len(rows)}
	if o.Type == "table" {
		// 表格按原始行导出，x/y/group-by 不参与渲染，直接忽略。
		f, err := os.Create(o.OutFile)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		qr := &types.QueryResult{Columns: cols, Rows: rows, RowCount: len(rows)}
		if o.Format == "xlsx" {
			return res, formatter.Write(f, "xlsx", qr)
		}
		return res, formatter.Write(f, "markdown", qr)
	}
	if o.Format != "" && o.Format != "png" && o.Format != "svg" {
		return nil, types.Errorf(types.CodeUsage, "--format %s 不适用于图表", o.Format).
			WithHint("图表请用 png / svg；md / xlsx 仅用于 --type table")
	}
	if o.Type != "bar" && o.Type != "line" && o.Type != "pie" {
		return nil, types.Errorf(types.CodeUsage, "unsupported chart type %q", o.Type).
			WithHint("可选: bar / line / pie / table")
	}
	missing := o.Missing
	if missing == "" {
		missing = "zero"
	}
	if missing != "zero" && missing != "gap" {
		return nil, types.Errorf(types.CodeUsage, "--missing %q 无效", o.Missing).
			WithHint("可选: zero（默认，缺失组合补 0）/ gap（缺失组合置空，line 断线）")
	}
	if missing == "gap" && o.Type != "line" {
		return nil, types.Errorf(types.CodeUsage, "--missing gap 仅适用于 line").
			WithHint("bar 没有断点语义（缺失的柱子本来就不可见），请用 --type line 或 --missing zero")
	}
	if len(o.Y) == 0 {
		return nil, types.Errorf(types.CodeUsage, "缺少 --y 数值列").
			WithHint("用 --y col 或 --y col1,col2 指定一个或多个数值列")
	}
	avail := "available columns: " + strings.Join(cols, ", ")
	seenY := map[string]bool{}
	for _, f := range o.Y {
		if seenY[f] {
			return nil, types.Errorf(types.CodeUsage, "--y 列重复: %q", f)
		}
		seenY[f] = true
		if !contains(cols, f) {
			return nil, types.Errorf(types.CodeExec, "field not found: %q", f).WithDetail(avail)
		}
	}
	if !contains(cols, o.X) {
		return nil, types.Errorf(types.CodeExec, "field not found: %q", o.X).WithDetail(avail)
	}
	if o.GroupBy != "" {
		if len(o.Y) > 1 {
			return nil, types.Errorf(types.CodeUsage, "--group-by 与多 --y 不能同时使用").
				WithHint("--group-by 用于长表透视，多 --y 用于宽表直取，二者选其一")
		}
		if o.GroupBy == o.X {
			return nil, types.Errorf(types.CodeUsage, "--group-by 不能与 --x 相同")
		}
		if o.GroupBy == o.Y[0] {
			return nil, types.Errorf(types.CodeUsage, "--group-by 不能与 --y 相同")
		}
		if !contains(cols, o.GroupBy) {
			return nil, types.Errorf(types.CodeExec, "field not found: %q", o.GroupBy).WithDetail(avail)
		}
	}
	if o.Type == "pie" && (o.GroupBy != "" || len(o.Y) > 1) {
		return nil, types.Errorf(types.CodeUsage, "pie 不支持多系列（--group-by / 多 --y）").
			WithHint("请改用 --type bar 或 line；饼图请先用 SQL 聚合到单一系列")
	}
	if len(rows) == 0 {
		return nil, types.Errorf(types.CodeExec, "no data to plot")
	}
	if len(rows) > maxPoints {
		return nil, types.Errorf(types.CodeExec, "too many data points (%d > %d)", len(rows), maxPoints).
			WithHint("在查询中加 LIMIT 或先聚合")
	}
	xs := make([]string, len(rows))
	for i, r := range rows {
		xv, _ := r.Get(o.X)
		xs[i] = formatter.Cell(xv)
	}
	var series [][]float64
	var names []string
	if o.GroupBy != "" {
		var err error
		xs, names, series, err = pivot(rows, o.X, o.GroupBy, o.Y[0], missing == "gap")
		if err != nil {
			return nil, err
		}
	} else {
		series = make([][]float64, len(o.Y))
		for yi, col := range o.Y {
			vals := make([]float64, len(rows))
			for i, r := range rows {
				yv, _ := r.Get(col)
				f, ok := toFloat(yv)
				if !ok {
					return nil, types.Errorf(types.CodeExec, "field %q is not numeric (row %d: %v)", col, i+1, yv).
						WithDetail(avail)
				}
				vals[i] = f
			}
			series[yi] = vals
		}
		if len(o.Y) > 1 {
			names = append([]string(nil), o.Y...)
		}
	}
	if len(names) == 1 {
		// group-by 只透出一个系列时，与不传 group-by 完全一致：不出图例、不加顶部留白。
		names = nil
	}
	res.Series = names
	if missing == "gap" {
		res.Missing = "gap"
	}

	opts := []charts.OptionFunc{charts.TitleOptionFunc(charts.TitleOption{Text: o.Title})}
	if o.Format == "svg" {
		opts = append(opts, charts.SVGTypeOption())
	} else {
		opts = append(opts, charts.PNGTypeOption())
	}
	extra := append([]string{o.X}, o.Y...)
	extra = append(extra, names...)
	if o.GroupBy != "" {
		extra = append(extra, o.GroupBy)
	}
	needCJK := hasNonASCII(o.Title, xs, extra...)
	if o.FontPath != "" || needCJK {
		src, data, err := ResolveFont(o.FontPath, o.FontDirs)
		switch {
		case err != nil && o.FontPath != "":
			return nil, types.Errorf(types.CodeNotFound, "cannot load font: %v", err).
				WithHint("需要含 TrueType 轮廓的 .ttf/.ttc（CFF 的 .otf 不支持）")
		case err != nil:
			res.Warnings = append(res.Warnings, fmt.Sprintf("DTOOL_FONT 加载失败：%v", err))
		case data == nil:
			res.Warnings = append(res.Warnings, "标题/标签含非 ASCII 字符，但未找到可用的中文字体；可用 --font 或配置文件 font 指定")
		default:
			if err := charts.InstallFont("dtool-font", data); err != nil {
				return nil, types.Errorf(types.CodeExec, "invalid font %s: %v", src, err)
			}
			opts = append(opts, charts.FontFamilyOptionFunc("dtool-font"))
			res.Font = src
			if _, latin := fontCoverage(data); !latin {
				res.Warnings = append(res.Warnings, fmt.Sprintf("字体 %s 缺少数字/拉丁字形，坐标轴数字可能显示为方框", src))
			}
		}
	}

	var p *charts.Painter
	var err error
	switch o.Type {
	case "bar", "line":
		p, err = renderSeries(o.Type, xs, series, names, o.Title, opts)
	case "pie":
		// 扇区标签已带名称与占比，隐藏图例以免与标题重叠
		hide := false
		p, err = charts.PieRender(series[0], append(opts, charts.LegendOptionFunc(charts.LegendOption{Data: xs, Show: &hide}),
			charts.PieSeriesShowLabel())...)
	}
	if err != nil {
		return nil, err
	}
	b, err := p.Bytes()
	if err != nil {
		return nil, err
	}
	return res, os.WriteFile(o.OutFile, b, 0o644)
}

// renderSeries 渲染柱状图/折线图；names 为空时保持单系列行为，非空时按系列名出图例。
func renderSeries(typ string, xs []string, series [][]float64, names []string, title string, opts []charts.OptionFunc) (*charts.Painter, error) {
	sl := charts.NewSeriesListDataFromValues(series, typ)
	for i, n := range names {
		sl[i].Name = n
	}
	base := charts.ChartOption{SeriesList: sl, XAxis: charts.NewXAxisOption(xs)}
	if len(names) != 0 {
		// go-charts 把图例与标题画在同一行；这里把图例挪到标题下方。
		// 另外，换行后的图例高度会被低估约 20px，用 legend 的 Bottom padding 补回来，
		// 否则图例会与 Y 轴顶部刻度重叠。
		legend := charts.LegendOption{Data: names}
		if title != "" {
			legend.Top = "26"
			legend.Padding = charts.Box{Top: 5, Bottom: 20}
		}
		base.Legend = legend
	}
	return charts.Render(base, opts...)
}

// label 把单元格转成图表标签；空值显式标注，避免坐标轴或图例出现空白项。
func label(v any) string {
	if s := formatter.Cell(v); s != "" {
		return s
	}
	return "(空)"
}

// pivot 把长表 (x, group, y) 透视成「X 分类 × group 系列」的矩阵。
// 分类与系列均按输入顺序（首次出现）排列，不重排，用户的 ORDER BY 即最终顺序；
// 缺失组合默认补 0，gap 为 true 时置为 go-charts 的空值哨兵（line 断线）；
// 同一个 (x, group) 出现多次无法确定格子取值，直接报错并给出聚合提示。
func pivot(rows []types.Row, x, groupBy, y string, gap bool) (xs, names []string, series [][]float64, err error) {
	xIdx := map[string]int{}
	gIdx := map[string]int{}
	cell := map[[2]int]float64{}
	seen := map[[2]int]int{} // 首次出现的行号（1-based）
	dups := 0
	var dupMsg string
	for i, r := range rows {
		yv, _ := r.Get(y)
		f, ok := toFloat(yv)
		if !ok {
			return nil, nil, nil, types.Errorf(types.CodeExec,
				"field %q is not numeric (row %d: %v)", y, i+1, yv)
		}
		xv, _ := r.Get(x)
		gv, _ := r.Get(groupBy)
		xk, gk := label(xv), label(gv)
		xi, ok := xIdx[xk]
		if !ok {
			xi = len(xs)
			xIdx[xk] = xi
			xs = append(xs, xk)
		}
		gi, ok := gIdx[gk]
		if !ok {
			gi = len(names)
			gIdx[gk] = gi
			names = append(names, gk)
		}
		k := [2]int{xi, gi}
		if first, dup := seen[k]; dup {
			dups++
			if dupMsg == "" {
				dupMsg = fmt.Sprintf("%s=%q、%s=%q（行 %d 与行 %d）", x, xk, groupBy, gk, first, i+1)
			}
		} else {
			seen[k] = i + 1
		}
		cell[k] = f
	}
	if dups != 0 {
		return nil, nil, nil, types.Errorf(types.CodeExec,
			"%d 个 (%s, %s) 组合重复出现，如 %s", dups, x, groupBy, dupMsg).
			WithHint(fmt.Sprintf("每个 (X, group) 格子只能有一个值：请先在 SQL 里聚合（SUM/AVG/COUNT），并 GROUP BY %s, %s", x, groupBy))
	}
	if len(names) > maxSeries {
		return nil, nil, nil, types.Errorf(types.CodeExec,
			"too many series for --group-by %q (%d > %d)", groupBy, len(names), maxSeries).
			WithHint("检查该字段是否选错，或先用 SQL 把基数降下来")
	}
	series = make([][]float64, len(names))
	for gi := range series {
		series[gi] = make([]float64, len(xs))
		for xi := range xs {
			k := [2]int{xi, gi}
			if v, ok := cell[k]; ok {
				series[gi][xi] = v
			} else if gap {
				series[gi][xi] = charts.GetNullValue()
			}
		}
	}
	return xs, names, series, nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func hasNonASCII(title string, xs []string, extra ...string) bool {
	all := append(append([]string{title}, xs...), extra...)
	for _, s := range all {
		for _, r := range s {
			if r > 127 {
				return true
			}
		}
	}
	return false
}
