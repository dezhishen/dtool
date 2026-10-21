package cmd

import (
	"path/filepath"
	"strings"
	"testing"
)

// 保证示例数据源与示例脚本依赖的结构不会悄悄失效。
func TestExampleDataSources(t *testing.T) {
	abs := func(name string) string {
		p, err := filepath.Abs(filepath.Join("..", "examples", "data", name))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	sales, hr, messy := abs("sales.xlsx"), abs("hr.xlsx"), abs("messy.xlsx")
	studio := abs("studio.xlsx")
	t.Chdir(t.TempDir())

	conv := func(file, sheet, name string) map[string]any {
		m, err := run(t, "convert", "--input", file, "--sheet", sheet, "--name", name)
		if err != nil {
			t.Fatalf("convert %s/%s: %v %v", file, sheet, m, err)
		}
		return m
	}
	if m := conv(sales, "订单", "orders"); m["record_count"].(float64) != 120 {
		t.Fatalf("orders: %v", m)
	}
	if m := conv(sales, "客户", "customers"); m["record_count"].(float64) != 15 {
		t.Fatalf("customers: %v", m)
	}
	if m := conv(hr, "员工", "hr"); m["record_count"].(float64) != 60 || m["warnings"] != nil {
		t.Fatalf("hr: %v", m)
	}
	m := conv(messy, "原始数据", "messy")
	warn := strings.Join(toStrings(m["warnings"]), "|")
	for _, want := range []string{"col_2", "名称_2", "合并单元格"} {
		if !strings.Contains(warn, want) {
			t.Errorf("messy warnings %q missing %q", warn, want)
		}
	}
	if m["record_count"].(float64) != 4 {
		t.Errorf("messy should skip the blank row: %v", m["record_count"])
	}

	// 示例 1：JOIN 与排除退款
	q, err := run(t, "query", "--sql", `SELECT c."客户名称", SUM(o."金额") AS t FROM orders o JOIN customers c ON o."客户ID" = c."客户ID"
		GROUP BY c."客户名称" ORDER BY t DESC LIMIT 5`)
	if err != nil || q["row_count"].(float64) != 5 {
		t.Fatalf("join: %v %v", q, err)
	}
	// 示例 2：前导零保持文本、绩效可空、日期可比较
	q, err = run(t, "query", "--rows", "--sql", `SELECT MIN("工号") AS first_id, COUNT(*) - COUNT("绩效") AS no_rating, SUM("入职日期" >= '2026-01-01') AS new_hires FROM hr`)
	if err != nil {
		t.Fatal(err)
	}
	row := q["rows"].([]any)[0].(map[string]any)
	if row["first_id"] != "00101" || row["no_rating"].(float64) != row["new_hires"].(float64) || row["no_rating"].(float64) == 0 {
		t.Fatalf("hr invariants: %v", row)
	}
	// 示例 3：前导零手机号、混合类型列按文本、NULLIF 转 NULL
	q, err = run(t, "query", "--rows", "--sql", `SELECT "手机号", CAST(NULLIF("金额", 'N/A') AS REAL) AS a FROM messy ORDER BY "编号"`)
	if err != nil {
		t.Fatal(err)
	}
	rows := q["rows"].([]any)
	if rows[0].(map[string]any)["手机号"] != "013800138000" || rows[2].(map[string]any)["a"] != nil {
		t.Fatalf("messy rows: %v", rows)
	}
	// 示例 4 的月度聚合覆盖 1–6 月
	q, err = run(t, "query", "--sql", `SELECT substr("日期", 1, 7) AS m, SUM("金额") AS s FROM orders GROUP BY 1 ORDER BY 1`)
	if err != nil || q["row_count"].(float64) != 6 {
		t.Fatalf("monthly: %v %v", q, err)
	}
	// studio.xlsx：docs/scenarios/studio.md 的四张表基线。文档里的「两单亏损」
	// 「三成报销没有项目归属」都建立在这些行数上，数据一改就得重新核对文档。
	for _, c := range []struct {
		sheet string
		name  string
		n     float64
	}{
		{"员工", "studio_emp", 10},
		{"项目", "studio_proj", 40},
		{"报销", "studio_exp", 60},
		{"考勤", "studio_att", 90},
	} {
		if m := conv(studio, c.sheet, c.name); m["record_count"].(float64) != c.n || m["warnings"] != nil {
			t.Fatalf("studio/%s: %v", c.sheet, m)
		}
	}
	// 文档口径：毛利 = 合同金额 − 投入工时 × 负责人月薪/176 − 直接费用。
	// 亏损单为 0 说明"两单刻意亏损"失效了；未结项的结束日期必须保持 NULL。
	q, err = run(t, "query", "--rows", "--sql", `SELECT
			COUNT(*) FILTER (WHERE p."合同金额" - p."投入工时" * e."月薪" / 176.0
				- COALESCE(f."直接费用", 0) < 0) AS loss,
			COUNT(*) FILTER (WHERE p."结束日期" IS NULL) AS open_cnt
		FROM studio_proj p JOIN studio_emp e ON p."负责人ID" = e."员工ID"
		LEFT JOIN (SELECT "项目ID", SUM("金额") AS "直接费用" FROM studio_exp
			WHERE "项目ID" IS NOT NULL GROUP BY 1) f ON f."项目ID" = p."项目ID"`)
	if err != nil {
		t.Fatal(err)
	}
	row = q["rows"].([]any)[0].(map[string]any)
	if row["loss"].(float64) != 2 || row["open_cnt"].(float64) != 8 {
		t.Fatalf("studio 项目不变量: %v", row)
	}
	// 报销要留出「公共开支没有项目归属」的部分，否则按项目摊费用就看不出差别。
	q, err = run(t, "query", "--rows", "--sql",
		`SELECT COUNT(*) FILTER (WHERE "项目ID" IS NULL) AS no_proj FROM studio_exp`)
	if err != nil {
		t.Fatal(err)
	}
	if n := q["rows"].([]any)[0].(map[string]any)["no_proj"].(float64); n < 10 {
		t.Fatalf("studio 报销无项目归属只有 %v 条", n)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
