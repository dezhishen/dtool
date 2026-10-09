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

const maxPoints = 1000

type Options struct {
	Type     string // bar / line / pie / table
	X, Y     string
	Title    string
	Format   string   // png / svg
	FontPath string   // 可选 TTF，用于显示中文
	OutFile  string   // 输出文件（绝对路径，扩展名由调用方根据 Ext 决定）
	FontDirs []string // 系统字体搜索目录，nil 表示使用平台默认
}

type Result struct {
	Points   int
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

// Render 校验字段并渲染图表/表格到 o.OutFile。
func Render(cols []string, rows []types.Row, o Options) (*Result, error) {
	res := &Result{Points: len(rows)}
	if o.Type == "table" {
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
	avail := "available columns: " + strings.Join(cols, ", ")
	for _, f := range []string{o.X, o.Y} {
		if !contains(cols, f) {
			return nil, types.Errorf(types.CodeExec, "field not found: %q", f).WithDetail(avail)
		}
	}
	if len(rows) == 0 {
		return nil, types.Errorf(types.CodeExec, "no data to plot")
	}
	if len(rows) > maxPoints {
		return nil, types.Errorf(types.CodeExec, "too many data points (%d > %d)", len(rows), maxPoints).
			WithHint("在查询中加 LIMIT 或先聚合")
	}
	xs := make([]string, len(rows))
	ys := make([]float64, len(rows))
	for i, r := range rows {
		xv, _ := r.Get(o.X)
		xs[i] = formatter.Cell(xv)
		yv, _ := r.Get(o.Y)
		f, ok := toFloat(yv)
		if !ok {
			return nil, types.Errorf(types.CodeExec, "field %q is not numeric (row %d: %v)", o.Y, i+1, yv).
				WithDetail(avail)
		}
		ys[i] = f
	}

	opts := []charts.OptionFunc{charts.TitleOptionFunc(charts.TitleOption{Text: o.Title})}
	if o.Format == "svg" {
		opts = append(opts, charts.SVGTypeOption())
	} else {
		opts = append(opts, charts.PNGTypeOption())
	}
	needCJK := hasNonASCII(o.Title, xs, o.X, o.Y)
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
	case "bar":
		p, err = charts.BarRender([][]float64{ys}, append(opts, charts.XAxisOptionFunc(charts.NewXAxisOption(xs)))...)
	case "line":
		p, err = charts.LineRender([][]float64{ys}, append(opts, charts.XAxisOptionFunc(charts.NewXAxisOption(xs)))...)
	case "pie":
		// 扇区标签已带名称与占比，隐藏图例以免与标题重叠
		hide := false
		p, err = charts.PieRender(ys, append(opts, charts.LegendOptionFunc(charts.LegendOption{Data: xs, Show: &hide}),
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
