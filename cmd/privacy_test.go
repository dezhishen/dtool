package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeJSON(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStrongPrivacyDefault(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	data := filepath.Join(dir, "d.json")
	writeJSON(t, data, `[{"region":"A","n":1},{"region":"B","n":2}]`)

	// 结果类命令默认只给交付清单
	m, err := runRaw(t, "query", "--sql", `SELECT region, n FROM "d.json"`)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"columns", "rows", "row_count", "result_file"} {
		if _, ok := m[k]; ok {
			t.Fatalf("强隐私 stdout 不应包含 %q: %v", k, m)
		}
	}
	arts, ok := m["artifacts"].([]any)
	if !ok || len(arts) != 1 {
		t.Fatalf("artifacts: %v", m)
	}
	art := arts[0].(map[string]any)
	if art["role"] != "result" || art["mime"] != "application/json" {
		t.Fatalf("artifact: %v", art)
	}
	b, err := os.ReadFile(art["path"].(string))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if art["sha256"] != hex.EncodeToString(sum[:]) || art["bytes"].(float64) != float64(len(b)) {
		t.Fatalf("artifact digest mismatch: %v", art)
	}
	if m["send"].(map[string]any)["mode"] != "attachment" {
		t.Fatalf("send: %v", m["send"])
	}
	id := m["action_id"].(string)

	// 转换也只给 data/schema 文件位置，不给 record_count/columns
	conv, err := runRaw(t, "convert", "--input", xlsx(t, dir), "--name", "ds")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := conv["record_count"]; ok {
		t.Fatalf("强隐私 convert 不应包含 record_count: %v", conv)
	}
	roles := map[string]bool{}
	for _, a := range conv["artifacts"].([]any) {
		roles[a.(map[string]any)["role"].(string)] = true
	}
	if !roles["data"] || !roles["schema"] {
		t.Fatalf("convert artifacts: %v", conv["artifacts"])
	}

	// 图表同样只给 chart 文件
	viz, err := runRaw(t, "visualize", "--input", "latest:query", "--type", "bar",
		"--x", "region", "--y", "n", "--format", "svg")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := viz["points"]; ok {
		t.Fatalf("强隐私 visualize 不应包含 points: %v", viz)
	}
	if a := viz["artifacts"].([]any)[0].(map[string]any); a["role"] != "chart" || a["mime"] != "image/svg+xml" {
		t.Fatalf("chart artifact: %v", a)
	}

	// pipeline 同样只给 result/chart 文件
	pipe, err := runRaw(t, "pipeline", "--input", data, "--sql", "SELECT region, n FROM data",
		"--chart", "bar", "--x", "region", "--y", "n", "--format", "svg")
	if err != nil {
		t.Fatal(err)
	}
	proles := map[string]bool{}
	for _, a := range pipe["artifacts"].([]any) {
		proles[a.(map[string]any)["role"].(string)] = true
	}
	if !proles["result"] || !proles["chart"] {
		t.Fatalf("pipeline artifacts: %v", pipe["artifacts"])
	}

	// 数据类动作在默认强隐私下一律拒绝
	cases := [][]string{
		{"query", "--rows", "--sql", `SELECT 1`},
		{"query", "--format", "csv", "--sql", `SELECT 1`},
		{"actions", "show", id, "--full"},
		{"actions", "output", id},
	}
	for _, args := range cases {
		if got, err := runRaw(t, args...); err == nil || got["code"].(float64) != 6 {
			t.Errorf("%v should be forbidden: %v %v", args, got, err)
		}
	}

	// deliver 仍允许：只给位置与校验和，不读内容
	d, err := runRaw(t, "deliver", id)
	if err != nil {
		t.Fatal(err)
	}
	if len(d["artifacts"].([]any)) != 1 || d["send"].(map[string]any)["hint"] == "" {
		t.Fatalf("deliver: %v", d)
	}

	// 导出仍可做，但不能带出预览明细
	dump := filepath.Join(dir, "actions.json")
	if _, err := runRaw(t, "actions", "export", "--output", dump); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "\"preview\"") {
		t.Fatalf("strong 导出不应包含 preview: %s", b)
	}
}

func TestDatasetsStructureHasNoValues(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if _, err := runRaw(t, "convert", "--input", xlsx(t, dir), "--name", "d"); err != nil {
		t.Fatal(err)
	}
	show, err := runRaw(t, "datasets", "show", "d")
	if err != nil {
		t.Fatal(err)
	}
	cols := show["schema"].(map[string]any)["columns"].([]any)
	if len(cols) == 0 {
		t.Fatalf("schema: %v", show)
	}
	for _, c := range cols {
		m := c.(map[string]any)
		for _, k := range []string{"enum", "samples", "min", "max"} {
			if _, ok := m[k]; ok {
				t.Fatalf("结构展示不应包含 %q: %v", k, m)
			}
		}
		if m["role"] == nil {
			t.Fatalf("结构缺少 role: %v", m)
		}
	}

	// 结构展示永远不带值，强隐私下也不能用 values 绕过
	if got, err := runRaw(t, "datasets", "values", "d", "--column", "region"); err == nil || got["code"].(float64) != 6 {
		t.Fatalf("datasets values should be forbidden: %v %v", got, err)
	}
}

func TestPrivacyCanOnlyBeChangedInConfig(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeJSON(t, filepath.Join(dir, "d.json"), `[{"region":"A","n":1},{"region":"B","n":2}]`)

	// 命令行没有 privacy 开关：传了就是未知参数
	if got, err := runRaw(t, "--privacy", "normal", "actions", "list"); err == nil || got["code"].(float64) != 2 {
		t.Fatalf("--privacy should not exist: %v %v", got, err)
	}

	// 用户显式改配置后才允许数据动作
	cfg := filepath.Join(dir, "normal.yaml")
	writeJSON(t, cfg, "privacy: normal\n")
	m, err := runRaw(t, "-c", cfg, "query", "--rows", "--sql", `SELECT region, n FROM "d.json"`)
	if err != nil || len(m["rows"].([]any)) != 2 {
		t.Fatalf("normal config should allow rows: %v %v", m, err)
	}

	// 非法值
	bad := filepath.Join(dir, "bad.yaml")
	writeJSON(t, bad, "privacy: weak\n")
	if got, err := runRaw(t, "-c", bad, "actions", "list"); err == nil || got["code"].(float64) != 2 {
		t.Fatalf("invalid privacy: %v %v", got, err)
	}
}

func TestDatasetsValuesNormal(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	cfg := filepath.Join(dir, "normal.yaml")
	writeJSON(t, cfg, "privacy: normal\n")
	if _, err := runRaw(t, "-c", cfg, "convert", "--input", xlsx(t, dir), "--name", "d"); err != nil {
		t.Fatal(err)
	}
	m, err := runRaw(t, "-c", cfg, "datasets", "values", "d", "--column", "region")
	if err != nil {
		t.Fatal(err)
	}
	vals := m["values"].([]any)
	if len(vals) == 0 || m["returned"].(float64) != float64(len(vals)) || m["action_id"] == nil {
		t.Fatalf("values: %v", m)
	}
	// 数据动作要留下可追溯的 Action
	show, err := runRaw(t, "-c", cfg, "actions", "show", m["action_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if purpose := show["input"].(map[string]any)["purpose"]; purpose != "distinct_values" {
		t.Fatalf("action purpose: %v", show["input"])
	}
}
