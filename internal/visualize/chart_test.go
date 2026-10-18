package visualize

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dezhishen/dtool/pkg/types"
	charts "github.com/vicanso/go-charts/v2"
	"github.com/wcharczuk/go-chart/v2/roboto"
)

// update 用于刷新 SVG golden：go test ./internal/visualize -update
var update = flag.Bool("update", false, "update golden files")

func rows(t *testing.T, js string) ([]string, []types.Row) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "r.json")
	os.WriteFile(p, []byte(js), 0o644)
	cols, r, err := LoadRows(p)
	if err != nil {
		t.Fatal(err)
	}
	return cols, r
}

const regionRows = `[{"region":"North","total":150},{"region":"South","total":250.5}]`

func opt(t *testing.T, typ, format string) Options {
	t.Helper()
	return Options{Type: typ, X: "region", Y: []string{"total"}, Title: "Sales", Format: format,
		OutFile: filepath.Join(t.TempDir(), "out."+Ext(typ, format)), FontDirs: []string{t.TempDir()}}
}

func TestLoadRowsColumnsUnion(t *testing.T) {
	cols, r := rows(t, `[{"b":1,"a":2},{"c":3,"a":1}]`)
	if strings.Join(cols, ",") != "b,a,c" || len(r) != 2 {
		t.Fatalf("cols = %v", cols)
	}
	p := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(p, []byte(`{"a":1}`), 0o644)
	if _, _, err := LoadRows(p); err == nil {
		t.Fatal("object accepted as rows")
	}
	if _, _, err := LoadRows(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestRenderAllTypesAndFormats(t *testing.T) {
	cols, r := rows(t, regionRows)
	for _, typ := range []string{"bar", "line", "pie"} {
		for _, format := range []string{"png", "svg"} {
			o := opt(t, typ, format)
			res, err := Render(cols, r, o)
			if err != nil {
				t.Fatalf("%s/%s: %v", typ, format, err)
			}
			if res.Points != 2 {
				t.Errorf("%s/%s points = %d", typ, format, res.Points)
			}
			b, _ := os.ReadFile(o.OutFile)
			switch format {
			case "png":
				if len(b) < 8 || string(b[1:4]) != "PNG" {
					t.Errorf("%s/png is not a PNG", typ)
				}
			case "svg":
				if !strings.Contains(string(b), "<svg") {
					t.Errorf("%s/svg is not an SVG", typ)
				}
			}
		}
	}
}

func TestRenderTableWritesMarkdown(t *testing.T) {
	cols, r := rows(t, regionRows)
	o := opt(t, "table", "png")
	if _, err := Render(cols, r, o); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(o.OutFile, ".md") {
		t.Fatalf("ext = %s", o.OutFile)
	}
	b, _ := os.ReadFile(o.OutFile)
	if !strings.Contains(string(b), "| region | total |") || !strings.Contains(string(b), "| South | 250.5 |") {
		t.Fatalf("markdown = %s", b)
	}
}

func TestRenderFieldValidation(t *testing.T) {
	cols, r := rows(t, `[{"region":"N","total":"abc"},{"region":"S","total":2}]`)
	cases := []struct {
		name string
		mut  func(*Options)
		want string
	}{
		{"missing x", func(o *Options) { o.X = "nope" }, "field not found"},
		{"missing y", func(o *Options) { o.Y = []string{"nope"} }, "field not found"},
		{"non numeric", func(o *Options) {}, "not numeric"},
		{"bad type", func(o *Options) { o.Type = "radar" }, "unsupported chart type"},
	}
	for _, c := range cases {
		o := opt(t, "bar", "png")
		c.mut(&o)
		_, err := Render(cols, r, o)
		var te *types.Error
		if !errors.As(err, &te) || !strings.Contains(te.Message, c.want) {
			t.Errorf("%s: err = %v", c.name, err)
			continue
		}
		if c.name == "missing x" && !strings.Contains(te.Detail, "region, total") {
			t.Errorf("detail should list available columns: %q", te.Detail)
		}
	}
	if _, err := Render(nil, nil, opt(t, "bar", "png")); err == nil {
		t.Error("empty data accepted")
	}
}

func TestRenderTooManyPoints(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i <= maxPoints; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		b, _ := json.Marshal(map[string]any{"region": i, "total": i})
		sb.Write(b)
	}
	sb.WriteString("]")
	cols, r := rows(t, sb.String())
	if _, err := Render(cols, r, opt(t, "bar", "png")); err == nil {
		t.Fatal("over-limit data accepted")
	}
}

func TestChineseWithoutFontWarnsAndWithFontUsesIt(t *testing.T) {
	stubCoverage(t)
	cols, r := rows(t, `[{"区域":"北","销量":3},{"区域":"南","销量":5}]`)
	o := Options{Type: "bar", X: "区域", Y: []string{"销量"}, Title: "销售", Format: "svg",
		OutFile: filepath.Join(t.TempDir(), "c.svg"), FontDirs: []string{t.TempDir()}}
	t.Setenv("DTOOL_FONT", "")
	res, err := Render(cols, r, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) == 0 || res.Font != "" {
		t.Fatalf("expected a no-font warning: %+v", res)
	}

	// 系统字体目录里放一个"中文字体"：自动发现并使用
	sys := t.TempDir()
	sysFont := write(t, sys, "wqy-microhei.ttc", makeTTC(roboto.Roboto))
	o.FontDirs = []string{sys}
	if res, err = Render(cols, r, o); err != nil || res.Font != sysFont || len(res.Warnings) != 0 {
		t.Fatalf("auto font: %+v %v", res, err)
	}

	// 显式字体优先于系统字体
	explicit := write(t, t.TempDir(), "mine.ttf", roboto.Roboto)
	o.FontPath = explicit
	if res, err = Render(cols, r, o); err != nil || res.Font != explicit {
		t.Fatalf("explicit font: %+v %v", res, err)
	}

	// 显式字体不可用必须报错而不是静默回退
	o.FontPath = filepath.Join(t.TempDir(), "missing.ttf")
	if _, err = Render(cols, r, o); err == nil {
		t.Fatal("missing explicit font accepted")
	}
}

func TestASCIIChartNeedsNoFont(t *testing.T) {
	cols, r := rows(t, regionRows)
	res, err := Render(cols, r, opt(t, "bar", "svg"))
	if err != nil || res.Font != "" || len(res.Warnings) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

const groupRows = `[
 {"region":"East","quarter":"Q1","total":10},
 {"region":"West","quarter":"Q1","total":20},
 {"region":"East","quarter":"Q2","total":30},
 {"region":"West","quarter":"Q2","total":40},
 {"region":"North","quarter":"Q1","total":50}
]`

func TestPivotOrderAndZeroFill(t *testing.T) {
	_, r := rows(t, groupRows)
	xs, names, series, err := pivot(r, "region", "quarter", "total", false)
	if err != nil {
		t.Fatal(err)
	}
	// 分类与系列都按输入首次出现顺序，不按字典序重排
	if want := []string{"East", "West", "North"}; !slices.Equal(xs, want) {
		t.Fatalf("categories = %v, want %v", xs, want)
	}
	if want := []string{"Q1", "Q2"}; !slices.Equal(names, want) {
		t.Fatalf("series = %v, want %v", names, want)
	}
	// North 没有 Q2 数据 → 补 0
	want := [][]float64{{10, 20, 50}, {30, 40, 0}}
	if len(series) != len(want) {
		t.Fatalf("series count = %d", len(series))
	}
	for i := range want {
		if !slices.Equal(series[i], want[i]) {
			t.Errorf("series[%d] = %v, want %v", i, series[i], want[i])
		}
	}
}

func TestPivotGap(t *testing.T) {
	_, r := rows(t, groupRows) // North 缺 Q2
	_, _, series, err := pivot(r, "region", "quarter", "total", true)
	if err != nil {
		t.Fatal(err)
	}
	if got := series[1][2]; got != charts.GetNullValue() {
		t.Fatalf("missing cell = %v, want null sentinel", got)
	}
	if got := series[0][2]; got != 50 {
		t.Fatalf("present cell = %v, want 50", got)
	}
}

func TestPivotDuplicateCombinationRejected(t *testing.T) {
	_, r := rows(t, `[
	 {"region":"East","quarter":"Q1","total":10},
	 {"region":"West","quarter":"Q1","total":20},
	 {"region":"East","quarter":"Q1","total":30}
	]`)
	_, _, _, err := pivot(r, "region", "quarter", "total", false)
	var te *types.Error
	if !errors.As(err, &te) || !strings.Contains(te.Message, "重复") {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"region=\"East\"", "quarter=\"Q1\"", "行 1 与行 3"} {
		if !strings.Contains(te.Message, want) {
			t.Errorf("message %q should contain %q", te.Message, want)
		}
	}
	if !strings.Contains(te.Hint, "GROUP BY region, quarter") {
		t.Errorf("hint should suggest aggregation: %q", te.Hint)
	}
}

func TestPivotEmptyLabels(t *testing.T) {
	_, r := rows(t, `[
	 {"region":null,"quarter":"Q1","total":1},
	 {"region":"East","quarter":null,"total":2}
	]`)
	xs, names, series, err := pivot(r, "region", "quarter", "total", false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"(空)", "East"}; !slices.Equal(xs, want) {
		t.Fatalf("categories = %v, want %v", xs, want)
	}
	if want := []string{"Q1", "(空)"}; !slices.Equal(names, want) {
		t.Fatalf("series = %v, want %v", names, want)
	}
	if !slices.Equal(series[0], []float64{1, 0}) || !slices.Equal(series[1], []float64{0, 2}) {
		t.Fatalf("series = %v", series)
	}
}

func TestPivotTooManySeries(t *testing.T) {
	var r []types.Row
	for i := 0; i <= maxSeries; i++ {
		r = append(r, types.Row{
			Columns: []string{"region", "quarter", "total"},
			Values:  []any{"East", fmt.Sprintf("Q%d", i), 1.0},
		})
	}
	_, _, _, err := pivot(r, "region", "quarter", "total", false)
	var te *types.Error
	if !errors.As(err, &te) || !strings.Contains(te.Message, "too many series") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(te.Hint, "基数") {
		t.Errorf("hint = %q", te.Hint)
	}
}

func TestPivotNonNumeric(t *testing.T) {
	_, r := rows(t, `[{"region":"East","quarter":"Q1","total":"abc"}]`)
	_, _, _, err := pivot(r, "region", "quarter", "total", false)
	if err == nil || !strings.Contains(err.Error(), "not numeric") {
		t.Fatalf("err = %v", err)
	}
}

func TestRenderGroupByRejected(t *testing.T) {
	cols, r := rows(t, groupRows)
	// pie 是单系列：group-by 明确拒绝
	_, err := Render(cols, r, Options{Type: "pie", X: "region", Y: []string{"total"}, GroupBy: "quarter", Format: "svg",
		OutFile: filepath.Join(t.TempDir(), "out.svg")})
	var te *types.Error
	if !errors.As(err, &te) || !strings.Contains(te.Message, "不支持多系列") || !strings.Contains(te.Hint, "bar 或 line") {
		t.Fatalf("pie: err = %v", err)
	}

	// table 按原始行导出，多系列参数直接忽略
	if _, err := Render(cols, r, Options{Type: "table", X: "region", Y: []string{"total"}, GroupBy: "quarter",
		Format: "svg", OutFile: filepath.Join(t.TempDir(), "out.md")}); err != nil {
		t.Fatalf("table should ignore multi-series params: %v", err)
	}

	o := opt(t, "bar", "svg")
	o.GroupBy = "month"
	_, err = Render(cols, r, o)
	if !errors.As(err, &te) || !strings.Contains(te.Message, `"month"`) || !strings.Contains(te.Detail, "quarter") {
		t.Fatalf("missing group field: %v", err)
	}
}

func TestRenderWithoutGroupByHasNoSeries(t *testing.T) {
	cols, r := rows(t, regionRows)
	res, err := Render(cols, r, opt(t, "bar", "svg"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Series) != 0 {
		t.Fatalf("single-series result should not report series: %v", res.Series)
	}
}

// group-by 只有一个唯一值时，渲染必须与不传 group-by 逐字节一致。
func TestRenderGroupBySingleSeriesMatchesPlain(t *testing.T) {
	cols, r := rows(t, `[
	 {"region":"North","quarter":"Q1","total":150},
	 {"region":"South","quarter":"Q1","total":250.5}
	]`)
	plain := Options{Type: "bar", X: "region", Y: []string{"total"}, Title: "Sales", Format: "svg",
		OutFile: filepath.Join(t.TempDir(), "plain.svg"), FontDirs: []string{t.TempDir()}}
	one := plain
	one.GroupBy = "quarter"
	one.OutFile = filepath.Join(t.TempDir(), "one.svg")

	if _, err := Render(cols, r, plain); err != nil {
		t.Fatal(err)
	}
	res, err := Render(cols, r, one)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Series) != 0 {
		t.Fatalf("single-group result should not report series: %v", res.Series)
	}
	b1, err := os.ReadFile(plain.OutFile)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := os.ReadFile(one.OutFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(normalizeEOL(b1), normalizeEOL(b2)) {
		t.Fatal("single-group group-by should render identically to a plain chart")
	}
}

func TestRenderGroupBySVGGolden(t *testing.T) {
	cols, r := rows(t, groupRows)
	for _, typ := range []string{"bar", "line"} {
		o := Options{Type: typ, X: "region", Y: []string{"total"}, GroupBy: "quarter",
			Title: "Sales by Quarter", Format: "svg",
			OutFile: filepath.Join(t.TempDir(), "chart.svg"), FontDirs: []string{t.TempDir()}}
		res, err := Render(cols, r, o)
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if want := []string{"Q1", "Q2"}; !slices.Equal(res.Series, want) {
			t.Fatalf("%s: series = %v", typ, res.Series)
		}
		b, err := os.ReadFile(o.OutFile)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range res.Series {
			if !strings.Contains(string(b), name) {
				t.Errorf("%s: legend %q missing from svg", typ, name)
			}
		}
		// 同样输入必须逐字节一致，否则 golden 断言没有意义
		again := o
		again.OutFile = filepath.Join(t.TempDir(), "again.svg")
		if _, err := Render(cols, r, again); err != nil {
			t.Fatal(err)
		}
		b2, err := os.ReadFile(again.OutFile)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(b, b2) {
			t.Fatalf("%s: svg output is not deterministic", typ)
		}
		golden := filepath.Join("testdata", "grouped_"+typ+".svg")
		if *update {
			if err := os.MkdirAll("testdata", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, b, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%s: %v (run: go test ./internal/visualize -update)", typ, err)
		}
		// Windows 的 autocrlf 可能把 golden checkout 成 CRLF；除换行符外仍要求逐字节一致。
		if !bytes.Equal(normalizeEOL(b), normalizeEOL(want)) {
			t.Errorf("%s: svg differs from %s (run with -update to refresh)", typ, golden)
		}
	}
}

// normalizeEOL 抹平 CRLF/LF 差异（Windows checkout 与 CI golden 比较用）。
func normalizeEOL(b []byte) []byte { return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")) }

func TestParseY(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"total", []string{"total"}},
		{" sales , profit ", []string{"sales", "profit"}},
		{"a,, b ,", []string{"a", "b"}},
		{"", nil},
	}
	for _, c := range cases {
		if got := ParseY(c.in); !slices.Equal(got, c.want) {
			t.Errorf("ParseY(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

const wideRows = `[
 {"month":"Jan","sales":10,"profit":3},
 {"month":"Feb","sales":20,"profit":5},
 {"month":"Mar","sales":15,"profit":8}
]`

func TestRenderMultiY(t *testing.T) {
	cols, r := rows(t, wideRows)
	o := Options{Type: "line", X: "month", Y: []string{"sales", "profit"},
		Title: "Sales and Profit", Format: "svg",
		OutFile: filepath.Join(t.TempDir(), "out.svg"), FontDirs: []string{t.TempDir()}}
	res, err := Render(cols, r, o)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"sales", "profit"}; !slices.Equal(res.Series, want) {
		t.Fatalf("series = %v", res.Series)
	}
	b, err := os.ReadFile(o.OutFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range res.Series {
		if !strings.Contains(string(b), name) {
			t.Errorf("legend %q missing from svg", name)
		}
	}
}

func TestRenderMultiYSVGGolden(t *testing.T) {
	cols, r := rows(t, wideRows)
	o := Options{Type: "line", X: "month", Y: []string{"sales", "profit"},
		Title: "Sales and Profit", Format: "svg",
		OutFile: filepath.Join(t.TempDir(), "out.svg"), FontDirs: []string{t.TempDir()}}
	if _, err := Render(cols, r, o); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(o.OutFile)
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "wide_line.svg")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run: go test ./internal/visualize -update)", err)
	}
	if !bytes.Equal(normalizeEOL(b), normalizeEOL(want)) {
		t.Errorf("svg differs from %s (run with -update to refresh)", golden)
	}
}

func TestRenderMissingValidation(t *testing.T) {
	cols, r := rows(t, groupRows)
	base := func() Options {
		return Options{Type: "line", X: "region", Y: []string{"total"}, GroupBy: "quarter", Format: "svg",
			OutFile: filepath.Join(t.TempDir(), "out.svg"), FontDirs: []string{t.TempDir()}}
	}
	cases := []struct {
		name string
		mut  func(*Options)
		code int
		msg  string
	}{
		{"invalid", func(o *Options) { o.Missing = "nope" }, types.CodeUsage, "无效"},
		{"bar gap", func(o *Options) { o.Type = "bar"; o.Missing = "gap" }, types.CodeUsage, "仅适用于 line"},
		{"pie gap", func(o *Options) { o.Type = "pie"; o.Missing = "gap" }, types.CodeUsage, "仅适用于 line"},
	}
	for _, c := range cases {
		o := base()
		c.mut(&o)
		_, err := Render(cols, r, o)
		var te *types.Error
		if !errors.As(err, &te) || te.Code != c.code || !strings.Contains(te.Message, c.msg) {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}

	// table 忽略 --missing
	o := base()
	o.Type, o.Missing, o.OutFile = "table", "gap", filepath.Join(t.TempDir(), "out.md")
	if _, err := Render(cols, r, o); err != nil {
		t.Fatalf("table should ignore --missing: %v", err)
	}
}

func TestRenderMissingGapSVGGolden(t *testing.T) {
	cols, r := rows(t, groupRows)
	o := Options{Type: "line", X: "region", Y: []string{"total"}, GroupBy: "quarter", Missing: "gap",
		Title: "Sales by Quarter", Format: "svg",
		OutFile: filepath.Join(t.TempDir(), "gap.svg"), FontDirs: []string{t.TempDir()}}
	res, err := Render(cols, r, o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Missing != "gap" {
		t.Fatalf("missing = %q", res.Missing)
	}
	b, err := os.ReadFile(o.OutFile)
	if err != nil {
		t.Fatal(err)
	}

	// 与补 0 的输出必须不同（否则 gap 没有生效）
	zero := o
	zero.Missing = "zero"
	zero.OutFile = filepath.Join(t.TempDir(), "zero.svg")
	if _, err := Render(cols, r, zero); err != nil {
		t.Fatal(err)
	}
	zb, err := os.ReadFile(zero.OutFile)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(normalizeEOL(b), normalizeEOL(zb)) {
		t.Fatal("gap output should differ from zero fill")
	}

	golden := filepath.Join("testdata", "grouped_line_gap.svg")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run: go test ./internal/visualize -update)", err)
	}
	if !bytes.Equal(normalizeEOL(b), normalizeEOL(want)) {
		t.Errorf("svg differs from %s (run with -update to refresh)", golden)
	}
}

func TestRenderMultiYValidation(t *testing.T) {
	cols, r := rows(t, wideRows)
	base := func() Options {
		return Options{Type: "bar", X: "month", Y: []string{"sales"}, Format: "svg",
			OutFile: filepath.Join(t.TempDir(), "out.svg"), FontDirs: []string{t.TempDir()}}
	}
	cases := []struct {
		name string
		mut  func(*Options)
		code int
		msg  string
	}{
		{"mutex", func(o *Options) { o.Y = []string{"sales", "profit"}; o.GroupBy = "month" },
			types.CodeUsage, "不能同时使用"},
		{"group = x", func(o *Options) { o.GroupBy = "month" }, types.CodeUsage, "不能与 --x 相同"},
		{"group = y", func(o *Options) { o.GroupBy = "sales" }, types.CodeUsage, "不能与 --y 相同"},
		{"pie multi y", func(o *Options) { o.Type = "pie"; o.Y = []string{"sales", "profit"} },
			types.CodeUsage, "不支持多系列"},
		{"duplicate y", func(o *Options) { o.Y = []string{"sales", "sales"} },
			types.CodeUsage, "列重复"},
		{"empty y", func(o *Options) { o.Y = nil }, types.CodeUsage, "缺少 --y"},
		{"missing y", func(o *Options) { o.Y = []string{"nope"} }, types.CodeExec, "field not found"},
		{"non numeric", func(o *Options) { o.Y = []string{"month"} }, types.CodeExec, "not numeric"},
	}
	for _, c := range cases {
		o := base()
		c.mut(&o)
		_, err := Render(cols, r, o)
		var te *types.Error
		if !errors.As(err, &te) || te.Code != c.code || !strings.Contains(te.Message, c.msg) {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}

	// 表格忽略多系列参数（x/y/group-by 不参与渲染）
	o := base()
	o.Type, o.Format = "table", "svg"
	o.Y = []string{"sales", "profit"}
	o.GroupBy, o.X = "month", ""
	o.OutFile = filepath.Join(t.TempDir(), "out.md")
	if _, err := Render(cols, r, o); err != nil {
		t.Fatalf("table should ignore multi-series params: %v", err)
	}
}
