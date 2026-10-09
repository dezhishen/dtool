package visualize

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dezhishen/dtool/pkg/types"
	"github.com/wcharczuk/go-chart/v2/roboto"
)

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
	return Options{Type: typ, X: "region", Y: "total", Title: "Sales", Format: format,
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
		{"missing y", func(o *Options) { o.Y = "nope" }, "field not found"},
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
	o := Options{Type: "bar", X: "区域", Y: "销量", Title: "销售", Format: "svg",
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
