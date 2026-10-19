package cmd

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wcharczuk/go-chart/v2/roboto"
	"github.com/xuri/excelize/v2"
)

// run 以给定参数执行 CLI，返回 stdout、退出错误。
// 既有用例默认在 normal 隐私下跑；强隐私用例用 runRaw。
func run(t *testing.T, args ...string) (map[string]any, error) {
	return runWith(t, true, args...)
}

func runRaw(t *testing.T, args ...string) (map[string]any, error) {
	return runWith(t, false, args...)
}

func runWith(t *testing.T, normal bool, args ...string) (map[string]any, error) {
	t.Helper()
	if normal {
		args = ensureNormalPrivacy(t, args)
	}
	t.Setenv("DTOOL_NO_REAPER", "1") // 单测不派生看护进程
	oldArgs, oldOut := os.Args, os.Stdout
	r, w, _ := os.Pipe()
	os.Args, os.Stdout = append([]string{"dtool"}, args...), w
	g, cfgFont, parsedMaxMemory = globalFlags{}, "", nil
	err := Execute("test")
	w.Close()
	os.Args, os.Stdout = oldArgs, oldOut
	out, _ := io.ReadAll(r)
	var m map[string]any
	if len(strings.TrimSpace(string(out))) > 0 {
		if jerr := json.Unmarshal(out, &m); jerr != nil {
			t.Fatalf("stdout is not JSON: %q", out)
		}
	}
	return m, err
}

// runStreams 与 run 相同，但同时捕获 stderr（用于断言提示信息）。
func runStreams(t *testing.T, args ...string) (string, error) {
	return runStreamsWith(t, true, args...)
}

func runStreamsRaw(t *testing.T, args ...string) (string, error) {
	return runStreamsWith(t, false, args...)
}

func runStreamsWith(t *testing.T, normal bool, args ...string) (string, error) {
	t.Helper()
	if normal {
		args = ensureNormalPrivacy(t, args)
	}
	t.Setenv("DTOOL_NO_REAPER", "1")
	oldArgs, oldStreams := os.Args, [2]*os.File{os.Stdout, os.Stderr}
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Args, os.Stdout, os.Stderr = append([]string{"dtool"}, args...), wOut, wErr
	g, cfgFont, parsedMaxMemory = globalFlags{}, "", nil
	err := Execute("test")
	wOut.Close()
	wErr.Close()
	os.Args, os.Stdout, os.Stderr = oldArgs, oldStreams[0], oldStreams[1]
	_, _ = io.ReadAll(rOut) // 排空 stdout，避免写端 EPIPE
	b, _ := io.ReadAll(rErr)
	rOut.Close()
	rErr.Close()
	return string(b), err
}

// ensureNormalPrivacy 让既有用例在 normal 隐私下运行：
// 用例自带 -c 时把 privacy: normal 写进该配置；否则追加一个临时配置。
func ensureNormalPrivacy(t *testing.T, args []string) []string {
	t.Helper()
	path := ""
	for i, a := range args {
		switch {
		case (a == "-c" || a == "--config") && i+1 < len(args):
			path = args[i+1]
		case strings.HasPrefix(a, "--config="):
			path = strings.TrimPrefix(a, "--config=")
		}
		if path != "" {
			break
		}
	}
	if path == "" {
		p := filepath.Join(t.TempDir(), "privacy.yaml")
		if err := os.WriteFile(p, []byte("privacy: normal\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return append([]string{"-c", p}, args...)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return args // 让原命令自己报配置缺失
	}
	if strings.Contains(string(b), "privacy:") {
		return args
	}
	if err := os.WriteFile(path, append(b, []byte("\nprivacy: normal\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	return args
}

// --load-mode 只对 JSON 装入（query / pipeline）生效：发给 convert 会被忽略，
// 但必须显式说一句，别让人以为「换了参数就能省内存」。
func TestLoadModeWarnsWhenInert(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	data := xlsx(t, dir)

	errOut, err := runStreams(t, "convert", "--load-mode", "stream", "--input", data, "--name", "d")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut, "--load-mode") || !strings.Contains(errOut, "已忽略") {
		t.Fatalf("convert 应提示 --load-mode 被忽略，实际 stderr = %q", errOut)
	}

	errOut, err = runStreams(t, "--load-mode", "stream", "datasets", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut, "已忽略") {
		t.Fatalf("datasets 也应提示忽略，实际 stderr = %q", errOut)
	}
}

// `actions sync` 是显式出口：入口的隐式回收被刻意跳过（见 openWorkspaceRaw），
// 否则它会永远报告「无需收敛」。
func TestActionsSyncCommand(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	data := xlsx(t, dir)
	if _, err := run(t, "convert", "--input", data, "--name", "d"); err != nil {
		t.Fatal(err)
	}

	// 伪造「被强杀」的现场：文件与索引都停在 running，pid 已不存在
	actionFiles, _ := filepath.Glob(filepath.Join(dir, ".dtool", "actions", "*.json"))
	if len(actionFiles) != 1 {
		t.Fatalf("actions = %v", actionFiles)
	}
	raw, err := os.ReadFile(actionFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	var a map[string]any
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	a["status"], a["pid"] = "running", 2147480000
	out, _ := json.Marshal(a)
	if err := os.WriteFile(actionFiles[0], out, 0o644); err != nil {
		t.Fatal(err)
	}
	idxPath := filepath.Join(dir, ".dtool", "index.json")
	raw, err = os.ReadFile(idxPath)
	if err != nil {
		t.Fatal(err)
	}
	var idx map[string]any
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatal(err)
	}
	for _, e := range idx["actions"].([]any) {
		m := e.(map[string]any)
		m["status"], m["pid"] = "running", 2147480000
	}
	out, _ = json.Marshal(idx)
	if err := os.WriteFile(idxPath, out, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := run(t, "actions", "sync")
	if err != nil {
		t.Fatal(err)
	}
	if got["stale"].(float64) != 1 || got["scanned"].(float64) != 1 || len(got["stale_ids"].([]any)) != 1 {
		t.Fatalf("sync 报告 = %v", got)
	}

	raw, err = os.ReadFile(actionFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	var after map[string]any
	if err := json.Unmarshal(raw, &after); err != nil {
		t.Fatal(err)
	}
	if after["status"] != "stale" {
		t.Fatalf("sync 后文件状态 = %v", after["status"])
	}
	if errMsg, _ := after["error"].(map[string]any)["message"].(string); !strings.Contains(errMsg, "进程已消失") {
		t.Fatalf("缺少可读原因：%v", after["error"])
	}
}

// 看护进程的入口命令：主进程已死时应把遗留的 running 落盘为 stale。
func TestReapCommandConverges(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	data := xlsx(t, dir)
	if _, err := run(t, "convert", "--input", data, "--name", "d"); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, ".dtool", "actions", "*.json"))
	if len(files) != 1 {
		t.Fatalf("actions = %v", files)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var a map[string]any
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	a["status"], a["pid"] = "running", 2147480000
	out, _ := json.Marshal(a)
	if err := os.WriteFile(files[0], out, 0o644); err != nil {
		t.Fatal(err)
	}
	// 被强杀时 Action 文件与 index.json 会同时停在 running，两处都要改才像真实现场
	idxPath := filepath.Join(dir, ".dtool", "index.json")
	raw, err = os.ReadFile(idxPath)
	if err != nil {
		t.Fatal(err)
	}
	var idx map[string]any
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatal(err)
	}
	for _, e := range idx["actions"].([]any) {
		m := e.(map[string]any)
		m["status"], m["pid"] = "running", 2147480000
	}
	out, _ = json.Marshal(idx)
	if err := os.WriteFile(idxPath, out, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := run(t, "__reap", "--workspace", filepath.Join(dir, ".dtool"), "--pid", "2147480000"); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var after map[string]any
	if err := json.Unmarshal(raw, &after); err != nil {
		t.Fatal(err)
	}
	if after["status"] != "stale" {
		t.Fatalf("__reap 后状态 = %v", after["status"])
	}
}

// meminfo 是「选档为什么是这样」的排查入口：必须给出探测来源、预算、阶梯与预演结论。
func TestMemInfoCommand(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	// 只需要体积有意义，内容不需要是合法 JSON（预演只做估算，不解析）
	if err := os.WriteFile(filepath.Join(dir, "d.json"), []byte(strings.Repeat("x", 10000)), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := run(t, "meminfo", "--source", "d=d.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"platform", "detected", "budget", "guard", "job_object",
		"usage_now", "plan", "ladder", "mem_policy"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("meminfo 缺少 %q：%v", k, got)
		}
	}
	plan := got["plan"].(map[string]any)
	if plan["verdict"] != "ok" || plan["chosen"] == "" {
		t.Fatalf("默认预算下应直接选档并通过：%v", plan)
	}
	if rungs := plan["rungs"].([]any); len(rungs) != 3 {
		t.Fatalf("预演应列出整条阶梯：%v", rungs)
	}

	// 预算明显不够：默认策略是「仍试最省档」，所以 verdict 是 risky 而不是 refused
	got, err = run(t, "meminfo", "--max-memory", "1K", "--source", "d=d.json")
	if err != nil {
		t.Fatal(err)
	}
	plan = got["plan"].(map[string]any)
	if plan["verdict"] != "risky" {
		t.Fatalf("预算不足应标 risky（默认仍会试最省档）：%v", plan)
	}
	if !strings.Contains(plan["reason"].(string), "最省档") {
		t.Fatalf("理由要说明会试哪一档：%v", plan["reason"])
	}
	if plan["chosen"] != "stream+disk" {
		t.Fatalf("应选最省档：%v", plan["chosen"])
	}

	// --mem-policy strict 时同样的预算应当直接拒绝
	got, err = run(t, "meminfo", "--max-memory", "1K", "--mem-policy", "strict", "--source", "d=d.json")
	if err != nil {
		t.Fatal(err)
	}
	if plan = got["plan"].(map[string]any); !strings.Contains(plan["reason"].(string), "strict") {
		t.Fatalf("strict 应说明不尝试：%v", plan["reason"])
	}

	if got["guard"].(map[string]any)["watchdog"] != true {
		t.Fatalf("给了预算就该开看门狗：%v", got["guard"])
	}
}

func xlsx(t *testing.T, dir string) string {
	t.Helper()
	f := excelize.NewFile()
	rows := [][]any{{"区域", "销量"}, {"北", 3}, {"南", 5}, {"北", 4}}
	for i, r := range rows {
		cell, _ := excelize.CoordinatesToCellName(1, i+1)
		f.SetSheetRow("Sheet1", cell, &r)
	}
	p := filepath.Join(dir, "d.xlsx")
	if err := f.SaveAs(p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConfigSelectsWorkspaceAndFont(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	os.WriteFile(filepath.Join(dir, "font.ttf"), roboto.Roboto, 0o644)
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("font: font.ttf\nworkspace: ws/.dtool\npreview_rows: 1\n"), 0o644)
	data := xlsx(t, dir)

	conv, err := run(t, "-c", "config.yaml", "convert", "--input", data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ws", ".dtool", "workspace.json")); err != nil {
		t.Fatalf("workspace from config not used: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".dtool")); err == nil {
		t.Fatal("default workspace was created despite config")
	}

	q, err := run(t, "-c", "config.yaml", "query", "--source", "d=action:"+conv["action_id"].(string),
		"--sql", `SELECT "区域", SUM("销量") AS total FROM d GROUP BY "区域" ORDER BY "区域"`)
	if err != nil {
		t.Fatal(err)
	}
	vz, err := run(t, "-c", "config.yaml", "visualize", "--input", "action:"+q["action_id"].(string),
		"--type", "bar", "--x", "区域", "--y", "total", "--title", "销量", "--format", "svg")
	if err != nil {
		t.Fatal(err)
	}
	if w, ok := vz["warnings"]; ok {
		t.Fatalf("font from config should silence the CJK warning: %v", w)
	}
	show, err := run(t, "-c", "config.yaml", "actions", "show", vz["action_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	font := show["output"].(map[string]any)["details"].(map[string]any)["font"].(string)
	if filepath.Base(font) != "font.ttf" {
		t.Fatalf("recorded font = %q", font)
	}

	// 命令行 --font 优先于配置文件
	other := filepath.Join(dir, "other.ttf")
	os.WriteFile(other, roboto.Roboto, 0o644)
	vz2, err := run(t, "-c", "config.yaml", "visualize", "--input", "action:"+q["action_id"].(string),
		"--type", "bar", "--x", "区域", "--y", "total", "--format", "svg", "--font", other)
	if err != nil {
		t.Fatal(err)
	}
	show, _ = run(t, "-c", "config.yaml", "actions", "show", vz2["action_id"].(string))
	if f := show["output"].(map[string]any)["details"].(map[string]any)["font"].(string); filepath.Base(f) != "other.ttf" {
		t.Fatalf("--font should win, got %q", f)
	}
}

func TestWorkspaceFlagBeatsConfig(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("workspace: from-config\n"), 0o644)
	if _, err := run(t, "-c", "config.yaml", "--workspace", "from-flag", "actions", "list"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "from-flag", "workspace.json")); err != nil {
		t.Fatal("--workspace ignored")
	}
	if _, err := os.Stat(filepath.Join(dir, "from-config")); err == nil {
		t.Fatal("config workspace used despite flag")
	}
}

func TestConfigErrorsAreStructured(t *testing.T) {
	t.Chdir(t.TempDir())
	m, err := run(t, "-c", "missing.yaml", "actions", "list")
	if err == nil || m["code"].(float64) != 3 {
		t.Fatalf("missing config: %v %v", m, err)
	}
	os.WriteFile("bad.yaml", []byte("nope: 1\n"), 0o644)
	m, err = run(t, "-c", "bad.yaml", "actions", "list")
	if err == nil || m["code"].(float64) != 2 {
		t.Fatalf("bad config: %v %v", m, err)
	}
}

func TestUsageAndNotFoundExitCodes(t *testing.T) {
	t.Chdir(t.TempDir())
	if m, err := run(t, "convert"); err == nil || m["code"].(float64) != 2 {
		t.Fatalf("missing flag: %v %v", m, err)
	}
	if m, err := run(t, "bogus"); err == nil || m["code"].(float64) != 2 {
		t.Fatalf("unknown command: %v %v", m, err)
	}
	if m, err := run(t, "convert", "--input", "nope.xlsx"); err == nil || m["code"].(float64) != 3 || m["action_id"] == nil {
		t.Fatalf("missing file: %v %v", m, err)
	}
	if m, err := run(t, "actions", "show", "01ARZ3NDEKTSV4RRFFQ69G5FAV"); err == nil || m["code"].(float64) != 3 {
		t.Fatalf("unknown action: %v %v", m, err)
	}
	if m, err := run(t, "query", "--sql", "SELECT 1", "--source", "bad"); err == nil || m["code"].(float64) != 2 {
		t.Fatalf("bad --source: %v %v", m, err)
	}
}

func TestActionsAnnotateTraceExportReindex(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	conv, err := run(t, "convert", "--input", xlsx(t, dir), "--tags", "a, b", "--notes", "n")
	if err != nil {
		t.Fatal(err)
	}
	id := conv["action_id"].(string)
	q, err := run(t, "query", "--from", "action:"+id, "--source", "d=action:"+id, "--sql", "SELECT COUNT(*) AS n FROM d")
	if err != nil {
		t.Fatal(err)
	}
	qid := q["action_id"].(string)

	if _, err := run(t, "actions", "annotate", qid, "--text", "含税", "--by", "ai-agent"); err != nil {
		t.Fatal(err)
	}
	show, _ := run(t, "actions", "show", qid)
	ann := show["annotations"].([]any)
	if len(ann) != 1 || ann[0].(map[string]any)["text"] != "含税" || show["derived_from"] != id {
		t.Fatalf("show: %v", show)
	}
	// 预览是明细：默认不打印，--full 显式要求
	if _, ok := show["output"].(map[string]any)["preview"]; ok {
		t.Fatalf("actions show 默认不应输出 preview: %v", show)
	}
	full, _ := run(t, "actions", "show", qid, "--full")
	if p, ok := full["output"].(map[string]any)["preview"].([]any); !ok || len(p) != 1 {
		t.Fatalf("actions show --full should include preview: %v", full)
	}
	meta := func() map[string]any { m, _ := run(t, "actions", "show", id); return m["metadata"].(map[string]any) }()
	if tags := meta["tags"].([]any); len(tags) != 2 || tags[1] != "b" || meta["notes"] != "n" {
		t.Fatalf("metadata: %v", meta)
	}
	tr, _ := run(t, "actions", "trace", id)
	if len(tr["downstream"].([]any)) != 1 {
		t.Fatalf("trace: %v", tr)
	}
	ex, err := run(t, "actions", "export")
	if err != nil || ex["count"].(float64) != 2 {
		t.Fatalf("export: %v %v", ex, err)
	}
	os.Remove(filepath.Join(dir, ".dtool", "index.json"))
	ri, err := run(t, "actions", "reindex")
	if err != nil || ri["count"].(float64) != 2 {
		t.Fatalf("reindex: %v %v", ri, err)
	}
	ls, _ := run(t, "actions", "list", "--type", "query", "--limit", "5")
	if ls["total"].(float64) != 1 {
		t.Fatalf("list: %v", ls)
	}
}

func TestNoRecordLeavesNoActions(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	m, err := run(t, "convert", "--input", xlsx(t, dir), "--no-record")
	if err != nil {
		t.Fatal(err)
	}
	if _, has := m["action_id"]; has && m["action_id"] != "" {
		t.Fatalf("action_id present with --no-record: %v", m["action_id"])
	}
	ls, _ := run(t, "actions", "list")
	if ls["total"].(float64) != 0 {
		t.Fatalf("actions recorded: %v", ls)
	}
}

func TestDatasetsListShowDeleteAndRef(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	data := xlsx(t, dir)
	if _, err := run(t, "convert", "--input", data, "--name", "销量"); err != nil {
		t.Fatal(err)
	}
	conv2, err := run(t, "convert", "--input", data, "--name", "销量")
	if err != nil {
		t.Fatal(err)
	}
	if conv2["data_file"] != ".dtool/datasets/销量/data.json" || conv2["schema_file"] != ".dtool/datasets/销量/data.schema.json" || conv2["updated_at"] == nil {
		t.Fatalf("convert: %v", conv2)
	}

	ls, err := run(t, "datasets", "list")
	if err != nil || ls["total"].(float64) != 1 {
		t.Fatalf("list: %v %v", ls, err)
	}
	d := ls["datasets"].([]any)[0].(map[string]any)
	if d["name"] != "销量" || d["version"] != nil || d["updated_at"] == nil || d["record_count"].(float64) != 3 || d["action_id"] != conv2["action_id"] {
		t.Fatalf("dataset: %v", d)
	}

	show, err := run(t, "datasets", "show", "销量")
	if err != nil {
		t.Fatal(err)
	}
	sch := show["schema"].(map[string]any)
	cols := sch["columns"].([]any)
	if sch["updated_at"] != d["updated_at"] || sch["generated_at"] != nil {
		t.Fatalf("schema updated_at %v != dataset %v", sch["updated_at"], d["updated_at"])
	}
	if len(cols) != 2 {
		t.Fatalf("show: %v", show)
	}
	if _, ok := show["preview"]; ok {
		t.Fatalf("datasets show 默认不应输出明细: %v", show)
	}
	for _, c := range cols {
		if _, ok := c.(map[string]any)["samples"]; ok {
			t.Fatalf("datasets show 的 schema 不应包含 samples: %v", c)
		}
	}

	// 默认只输出摘要；--rows 显式要求明细
	q, err := run(t, "query", "--sql", `SELECT SUM("销量") AS total FROM "销量"`)
	if err != nil || q["row_count"].(float64) != 1 {
		t.Fatalf("query summary: %v %v", q, err)
	}
	if _, ok := q["rows"]; ok {
		t.Fatalf("query 默认不应输出 rows: %v", q)
	}
	q, err = run(t, "query", "--rows", "--sql", `SELECT SUM("销量") AS total FROM "销量"`)
	if err != nil || q["rows"].([]any)[0].(map[string]any)["total"].(float64) != 12 {
		t.Fatalf("query by dataset name: %v %v", q, err)
	}
	q, err = run(t, "query", "--rows", "--source", "s=dataset:销量", "--sql", `SELECT COUNT(*) AS n FROM s`)
	if err != nil || q["rows"].([]any)[0].(map[string]any)["n"].(float64) != 3 {
		t.Fatalf("query via ref: %v %v", q, err)
	}

	if m, err := run(t, "datasets", "show", "nope"); err == nil || m["code"].(float64) != 3 {
		t.Fatalf("unknown dataset: %v %v", m, err)
	}
	if _, err := run(t, "datasets", "delete", "销量"); err != nil {
		t.Fatal(err)
	}
	if ls, _ := run(t, "datasets", "list"); ls["total"].(float64) != 0 {
		t.Fatalf("after delete: %v", ls)
	}
	if m, err := run(t, "datasets", "delete", "销量"); err == nil || m["code"].(float64) != 3 {
		t.Fatalf("double delete: %v %v", m, err)
	}
}

func TestUpdateFlagsValidateBeforeAnyNetworkAccess(t *testing.T) {
	t.Chdir(t.TempDir())
	cases := [][]string{
		{"--pre"},                                             // --pre 必须配合 --update
		{"--channel", "dev"},                                  // --channel 必须配合 --update / upgrade
		{"--update", "--channel", "bogus"},                    // 渠道名非法
		{"--update", "--channel", "dev", "--pre"},             // 渠道与 --pre 冲突
		{"upgrade", "--version", "1.0.0", "--channel", "dev"}, // 指定版本与渠道互斥
		{"upgrade", "--version", "not-a-version"},             // 版本号格式错误
		{"upgrade", "--version", "1.0.0", "--pre"},            // 二者互斥
		{"upgrade", "extra"},                                  // 不接受位置参数
	}
	for _, args := range cases {
		m, err := run(t, args...)
		if err == nil || m["code"].(float64) != 2 {
			t.Errorf("%v: %v %v", args, m, err)
		}
	}
}

func TestVersionCommandAndFlag(t *testing.T) {
	t.Chdir(t.TempDir())
	m, err := run(t, "version")
	if err != nil {
		t.Fatal(err)
	}
	// run() 以 Execute("test") 运行：版本被覆盖为 test，其余元数据来自构建信息
	if m["version"] != "test" || m["channel"] != "local" {
		t.Fatalf("version/channel: %v", m)
	}
	for _, k := range []string{"commit", "commit_date", "branch", "dirty", "build_date", "build_id", "build_url", "builder",
		"repo", "go", "compiler", "cgo", "os", "arch"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing field %q in %v", k, m)
		}
	}
	if m["builder"] != "local" || m["go"] == "" || m["os"] == "" {
		t.Errorf("%v", m)
	}
	if _, has := m["deps"]; has {
		t.Error("deps must be opt-in")
	}
	if m, err = run(t, "version", "--deps"); err != nil || m["deps"] == nil && m["go"] == "" {
		t.Errorf("--deps: %v %v", m, err)
	}
	if _, err := run(t, "version", "extra"); err == nil {
		t.Error("positional args accepted")
	}
}

func TestMaxMemoryFlag(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	data := xlsx(t, dir)

	// 非法容量 -> 参数错误
	if m, err := run(t, "--max-memory", "abc", "actions", "list"); err == nil || m["code"].(float64) != 2 {
		t.Fatalf("bad size: %v %v", m, err)
	}
	// 预算过小 -> 可读的失败，而不是被 OOM 杀掉；且失败也留下 Action 记录
	m, err := run(t, "--max-memory", "1K", "convert", "--input", data)
	if err == nil || m["code"].(float64) != 4 || m["action_id"] == nil {
		t.Fatalf("tiny budget: %v %v", m, err)
	}
	if !strings.Contains(m["hint"].(string), "--max-memory 0") {
		t.Fatalf("hint = %v", m["hint"])
	}
	// 关闭检查后正常完成
	if res, err := run(t, "--max-memory", "0", "convert", "--input", data); err != nil {
		t.Fatalf("guard off: %v %v", res, err)
	}
	// 预算充足时正常完成
	if res, err := run(t, "--max-memory", "4G", "convert", "--input", data, "--name", "big"); err != nil {
		t.Fatalf("enough: %v %v", res, err)
	}
}

func TestLoadModeFlagAndConfig(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	// 非法值 -> 参数错误
	if m, err := run(t, "--load-mode", "fast", "actions", "list"); err == nil || m["code"].(float64) != 2 {
		t.Fatalf("bad mode: %v %v", m, err)
	}
	if _, err := run(t, "convert", "--input", xlsx(t, dir), "--name", "d"); err != nil {
		t.Fatal(err)
	}
	// 三种模式结果一致
	for _, mode := range []string{"auto", "stream", "full"} {
		q, err := run(t, "--load-mode", mode, "query", "--rows", "--source", "d=dataset:d", "--sql", `SELECT COUNT(*) AS n FROM d`)
		if err != nil {
			t.Fatalf("mode %s: %v", mode, err)
		}
		if n := q["rows"].([]any)[0].(map[string]any)["n"].(float64); n != 3 {
			t.Fatalf("mode %s: n = %v", mode, n)
		}
	}
	// 配置文件里的 load_mode 生效（命令行未指定时）
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("load_mode: stream\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "-c", "config.yaml", "query", "--source", "d=dataset:d", "--sql", `SELECT COUNT(*) AS n FROM d`); err != nil {
		t.Fatalf("config load_mode: %v", err)
	}
	// 命令行优先于配置文件
	if _, err := run(t, "-c", "config.yaml", "--load-mode", "full", "query", "--source", "d=dataset:d", "--sql", `SELECT COUNT(*) AS n FROM d`); err != nil {
		t.Fatalf("flag should win: %v", err)
	}
}
func TestVisualizeGroupBy(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	data := filepath.Join(dir, "quarterly.json")
	os.WriteFile(data, []byte(`[
	 {"region":"East","quarter":"Q1","total":10},
	 {"region":"West","quarter":"Q1","total":20},
	 {"region":"East","quarter":"Q2","total":30},
	 {"region":"West","quarter":"Q2","total":40}
	]`), 0o644)

	got, err := run(t, "visualize", "--input", data, "--type", "bar",
		"--x", "region", "--y", "total", "--group-by", "quarter", "--format", "svg")
	if err != nil {
		t.Fatal(err)
	}
	if got["group_by"] != "quarter" {
		t.Fatalf("group_by = %v", got["group_by"])
	}
	series, _ := got["series"].([]any)
	if len(series) != 2 || series[0] != "Q1" || series[1] != "Q2" {
		t.Fatalf("series = %v", got["series"])
	}
	// Action input 记录系列数与系列名
	show, err := run(t, "actions", "show", got["action_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	input, _ := show["input"].(map[string]any)
	if input["series_count"].(float64) != 2 {
		t.Fatalf("action series_count = %v", input["series_count"])
	}
	if ss, _ := input["series"].([]any); len(ss) != 2 || ss[0] != "Q1" || ss[1] != "Q2" {
		t.Fatalf("action series = %v", input["series"])
	}
	b, err := os.ReadFile(got["file"].(string))
	if err != nil || !strings.Contains(string(b), "Q1") {
		t.Fatalf("chart file: %v", err)
	}

	// 饼图是单系列，带 group-by 必须明确拒绝
	if errOut, err := runStreams(t, "visualize", "--input", data, "--type", "pie",
		"--x", "region", "--y", "total", "--group-by", "quarter"); err == nil || !strings.Contains(errOut, "不支持多系列") {
		t.Fatalf("pie accepted --group-by: %v %q", err, errOut)
	}

	// pipeline 同一条龙也支持
	pipe, err := run(t, "pipeline", "--input", data, "--sql", "SELECT region, quarter, total FROM data",
		"--chart", "bar", "--x", "region", "--y", "total", "--group-by", "quarter", "--format", "svg")
	if err != nil {
		t.Fatal(err)
	}
	chart, _ := pipe["chart"].(map[string]any)
	if chart["group_by"] != "quarter" || len(chart["series"].([]any)) != 2 {
		t.Fatalf("pipeline chart = %v", pipe["chart"])
	}
	// 默认只输出摘要，不把明细带到 stdout
	if qres, ok := pipe["query"].(map[string]any); ok {
		if _, has := qres["rows"]; has {
			t.Fatalf("pipeline 默认不应输出 query.rows: %v", pipe["query"])
		}
	}
	pipeRows, err := run(t, "pipeline", "--rows", "--input", data, "--sql", "SELECT region, quarter, total FROM data",
		"--chart", "bar", "--x", "region", "--y", "total", "--group-by", "quarter", "--format", "svg")
	if err != nil {
		t.Fatal(err)
	}
	if qres, ok := pipeRows["query"].(map[string]any); !ok || len(qres["rows"].([]any)) != 4 {
		t.Fatalf("pipeline --rows should include rows: %v", pipeRows["query"])
	}
}

func TestVisualizeMultiY(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	data := filepath.Join(dir, "wide.json")
	os.WriteFile(data, []byte(`[
	 {"month":"Jan","sales":10,"profit":3},
	 {"month":"Feb","sales":20,"profit":5}
	]`), 0o644)

	got, err := run(t, "visualize", "--input", data, "--type", "line",
		"--x", "month", "--y", "sales,profit", "--format", "svg")
	if err != nil {
		t.Fatal(err)
	}
	series, _ := got["series"].([]any)
	if len(series) != 2 || series[0] != "sales" || series[1] != "profit" {
		t.Fatalf("series = %v", got["series"])
	}
	// Action input 的 y 必须是数组
	show, err := run(t, "actions", "show", got["action_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	ys, _ := show["input"].(map[string]any)["y"].([]any)
	if len(ys) != 2 || ys[0] != "sales" || ys[1] != "profit" {
		t.Fatalf("action input y = %#v", show["input"])
	}

	// 参数错误：互斥 / 同列 / pie 多系列，退出码 2
	for _, c := range []struct {
		name string
		args []string
		msg  string
	}{
		{"mutex", []string{"--type", "line", "--x", "month", "--y", "sales,profit", "--group-by", "month"}, "不能同时使用"},
		{"group=x", []string{"--type", "line", "--x", "month", "--y", "sales", "--group-by", "month"}, "不能与 --x 相同"},
		{"group=y", []string{"--type", "line", "--x", "month", "--y", "sales", "--group-by", "sales"}, "不能与 --y 相同"},
		{"pie", []string{"--type", "pie", "--x", "month", "--y", "sales,profit"}, "不支持多系列"},
	} {
		args := append([]string{"visualize", "--input", data}, c.args...)
		m, err := run(t, args...)
		if err == nil || m["code"].(float64) != float64(2) || !strings.Contains(m["error"].(string), c.msg) {
			t.Errorf("%s: %v %v", c.name, m, err)
		}
	}

	// 表格忽略多系列参数
	if _, err := run(t, "visualize", "--input", data, "--type", "table",
		"--y", "sales,profit", "--group-by", "month", "--format", "md"); err != nil {
		t.Fatalf("table should ignore multi-series params: %v", err)
	}
}

func TestVisualizeMissing(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	data := filepath.Join(dir, "gap.json")
	os.WriteFile(data, []byte(`[
	 {"region":"East","quarter":"Q1","total":10},
	 {"region":"West","quarter":"Q1","total":20},
	 {"region":"East","quarter":"Q2","total":30}
	]`), 0o644)

	got, err := run(t, "visualize", "--input", data, "--type", "line",
		"--x", "region", "--y", "total", "--group-by", "quarter", "--missing", "gap", "--format", "svg")
	if err != nil {
		t.Fatal(err)
	}
	if got["missing"] != "gap" {
		t.Fatalf("missing = %v", got["missing"])
	}
	show, err := run(t, "actions", "show", got["action_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if show["input"].(map[string]any)["missing"] != "gap" {
		t.Fatalf("action input missing = %v", show["input"])
	}

	// 参数错误：非法值 / bar 不支持 gap
	for _, c := range []struct {
		name string
		args []string
		msg  string
	}{
		{"invalid", []string{"--type", "line", "--x", "region", "--y", "total", "--group-by", "quarter", "--missing", "nope"}, "无效"},
		{"bar gap", []string{"--type", "bar", "--x", "region", "--y", "total", "--group-by", "quarter", "--missing", "gap"}, "仅适用于 line"},
	} {
		args := append([]string{"visualize", "--input", data}, c.args...)
		m, err := run(t, args...)
		if err == nil || m["code"].(float64) != 2 || !strings.Contains(m["error"].(string), c.msg) {
			t.Errorf("%s: %v %v", c.name, m, err)
		}
	}
}
