package query

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/dezhishen/dtool/internal/memguard"
	"github.com/dezhishen/dtool/pkg/types"
	_ "modernc.org/sqlite"
)

type Options struct {
	SQL     string
	Sources map[string]string // 别名 -> 文件绝对路径（用户显式绑定，不受沙箱限制）
	Roots   []string          // 沙箱允许读取的目录，同时作为相对路径解析的候选目录
	Sandbox bool
	MaxRows int
	Timeout time.Duration
	// MaxMemory 内存预算：nil 表示自动探测（cgroup / 系统可用内存）；指向 0 表示关闭检查。
	MaxMemory *uint64
	// LoadMode 决定 JSON → SQLite 的装入方式："" 或 auto 按内存预算自适应（见 ladder），
	// stream 流式，full 整块解析。显式指定即强制，只影响速度与峰值，不影响结果。
	LoadMode string
	// Store 决定 SQLite 库落在哪："" 或 auto 由预算选，memory 内存库（快），disk 磁盘库（峰值低）。
	Store string
	// MemPolicy 决定「所有档都预计放不下」时的行为："" 或 try 仍试最省档（失败记录在
	// Action 里，AI 可据此重试），strict 直接失败。
	MemPolicy string
	// PlanFile 是选档历史（.dtool/plans/samples.json）；空表示不读不写。
	PlanFile string
	Lookup   func(name string) (path string, ok bool) // 数据集名 -> 数据文件，可为 nil
}

var errMaxRows = errors.New("max rows exceeded")

// peakFactor 为各执行档的实测峰值倍率（保守上界，见 docs/PERFORMANCE.md）：
// full 整块解析实测 7.75×、取 13×；stream 逐行装入实测 1.4–2.4×、取 2×；
// stream+磁盘库实测 0.12–0.16×、取 0.3×（页缓存变成文件页，可被系统回收）。
const (
	fullPeakFactor   = 13
	streamPeakFactor = 2
	diskPeakFactor   = 0.3
	// autoStreamSize：体积达到该值时，即使预算够也不选整块解析（100MB 级输入
	// 整块解析要 1.3GB 峰值，留那么大余量没有意义，不如稳定走流式）。
	autoStreamSize = 32 << 20
)

// progressMinSize 可在测试中替换。
var progressMinSize = uint64(memguard.DefaultProgressMinSize)

// Run 重写表引用，把用到的 JSON 文件载入内存 SQLite（modernc，纯 Go）后执行查询。
// 载入前会按可用内存做预估，载入/查询期间有内存看门狗，超限时以普通错误退出并给出原因。
// Run 重写表引用，把用到的 JSON 文件载入 SQLite（modernc，纯 Go）后执行查询。
//
// 内存上限在这里的作用是**选执行档**，不是「过/不过」：按预算从快到省挑一档
// （full+memory → stream+memory → stream+disk），只有「连最省档都预计放不下」时才
// 触及失败路径，且默认策略是带着记录去试一次——失败会留在 Action 与选档历史里，
// AI 据此换更省档重试。操作者可以用 --load-mode / --store 强行指定（见 ladder）。
func Run(ctx context.Context, o Options) (*types.QueryResult, error) {
	sqlText, binds, err := Rewrite(o)
	if err != nil {
		return nil, err
	}

	mem := memguard.Budget(o.MaxMemory)
	var total uint64
	for _, b := range binds {
		total += memguard.SizeOf(b.Path)
	}
	plan, spec, hist, err := choosePlan(o, mem, total)
	if err != nil {
		return nil, err
	}
	if mem.Uncertain {
		// 预算不可信这件事要说两次：一次随结果/日志走，一次直接给用户。
		fmt.Fprintf(memguard.NoticeWriter,
			"警告：%s\n预算按本机空闲内存算，若其实受沙箱限制请用 --max-memory 指定上限；\n"+
				"本次已排除峰值较高的整块解析档，必要时用 --store disk 进一步压低峰值。\n",
			mem.Source)
	}
	memguard.Debugf("%s；数据源 %d 个共 %s；%s", mem.Describe(), len(binds),
		memguard.HumanSize(total), planNote(plan, plan.Forced))

	if mem.Threshold() > 0 {
		// 软上限：接近阈值时 GC 更积极，尽量不撞上 cgroup / Job Object 硬限制
		// （堆上限比可用预算更低：堆外的 SQLite 页缓存同样计入提交量，见 SoftLimit）
		defer debug.SetMemoryLimit(-1)
		debug.SetMemoryLimit(int64(mem.SoftLimit()))
	}
	ctx, stopWatch := memguard.Watch(ctx, mem.Threshold())
	defer stopWatch()
	memguard.Debugf("看门狗=%s（阈值 %s）；Go 堆软上限=%s（阈值 %s）", onOff(mem.Threshold() > 0),
		memguard.HumanSize(mem.Threshold()), onOff(mem.Threshold() > 0), memguard.HumanSize(mem.SoftLimit()))

	db, closeStore, err := openStore(ctx, spec.Store)
	if err != nil {
		return nil, err
	}
	defer closeStore()

	start := time.Now()
	loadErr := func() error {
		for _, b := range binds {
			size := memguard.SizeOf(b.Path)
			memguard.Progress(os.Stderr, progressMinSize, filepath.Base(b.Path), size,
				spec.Predicted(size), modeNote(spec.Mode))
			if err := loadTable(ctx, db, b, spec.Mode); err != nil {
				return wrapErr(ctx, o, fmt.Errorf("载入 %s: %w", filepath.Base(b.Path), err), "load")
			}
		}
		return nil
	}()
	// 记录这次实测：预计多少、实际峰值多少、跑没跑完。失败那条最有价值——它告诉
	// 下一次别再选这一档（fatal 崩溃时进程什么都不剩，只有工作区里的记录还在）。
	recordSample(hist, o.PlanFile, spec, total, plan.Predicted, start, spec.Store, mem, loadErr == nil)
	if loadErr != nil {
		return nil, loadErr
	}

	// --timeout 只约束查询：载入是本地的读写与 CPU 密集工作，大文件可能远超 60s，
	// 把它算进去会让默认值变成陷阱（载入本身由内存看门狗与 Ctrl+C 兜底）。
	qctx := ctx
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		qctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	res, err := collect(qctx, db, sqlText, o.MaxRows)
	if err != nil {
		return nil, wrapErr(qctx, o, err, phaseQuery)
	}
	res.Strategy = spec.Name
	res.StrategyNote = plan.Reason
	return res, nil
}

// choosePlan 选执行档：把「操作者的显式指定」与「预算 + 历史」合在一起判断。
func choosePlan(o Options, mem memguard.Memory, total uint64) (memguard.Plan, strategy, *memguard.History, error) {
	cands, forced, err := candidates(o.LoadMode, o.Store)
	if err != nil {
		return memguard.Plan{}, strategy{}, nil, err
	}
	// 上限读不到时（Windows Job 标志位设了、值却是 0）：Available 只是「本机空闲内存」，
	// 不是「允许你用的量」。此时排除峰值比输入大一个数量级的整块解析档——沙箱真限制
	// 256MB 而输入 118MB 时，整块解析会撞上不可恢复的硬上限。操作者显式指定时不干预。
	if mem.Uncertain && forced == "" {
		kept := make([]strategy, 0, len(cands))
		for _, c := range cands {
			if c.Mode == LoadFull {
				memguard.Debugf("上限读不到（%s）：排除 %s", mem.Source, c.Name)
				continue
			}
			kept = append(kept, c)
		}
		if len(kept) > 0 {
			cands = kept
		}
	}
	hist, err := memguard.LoadHistory(o.PlanFile)
	if err != nil {
		memguard.Debugf("选档历史读取失败（按无历史处理）：%v", err)
		hist = &memguard.History{}
	}
	plan, err := memguard.Choose(memguard.ChooseRequest{
		Size: total, Memory: mem, Candidates: candidateList(cands), Forced: forced,
		Policy: policyOf(o.MemPolicy), Samples: hist.Samples,
	})
	if err == nil && mem.Uncertain && !plan.Forced {
		plan.Reason = joinReason(plan.Reason,
			"Job Object 上限读不到（预算按本机空闲内存算，不可信），已排除整块解析档")
	}
	if err != nil {
		return memguard.Plan{}, strategy{}, nil, types.Errorf(types.CodeUsage, "%v", err)
	}
	spec := strategyByName(plan.Chosen.Name)
	if plan.Risky {
		if policyOf(o.MemPolicy) == memguard.PolicyStrict {
			return memguard.Plan{}, strategy{}, nil, types.Errorf(types.CodeExec,
				"预计内存不足，已按 --mem-policy strict 中止：%s", memguard.HumanSize(total)).
				WithDetail(fmt.Sprintf("最省档 %s 预计需 %s，阈值 %s（%s）；%s",
					spec.Name, memguard.HumanSize(plan.Predicted), memguard.HumanSize(plan.Threshold),
					mem.Source, rejectedNote(plan))).
				WithHint("拆分或裁剪输入后重试；确需强制运行时用 --load-mode/--store 指定档位，或 --max-memory 0 关闭检查")
		}
		// 默认 try：仍然试最省档，但先把话说清楚（失败会写进 Action，AI 可据此重试）。
		fmt.Fprintf(memguard.NoticeWriter,
			"警告：所有执行档的预计峰值都超出预算（%s），将按最省档 %s 试一次；\n"+
				"若失败，请换 --store disk、拆分输入或调大 --max-memory。\n",
			rejectedSummary(plan), spec.Name)
	}
	return plan, spec, hist, nil
}

// policyOf 把 Options 里的策略字符串翻成枚举（空 = try）。
func policyOf(s string) memguard.Policy {
	p, err := memguard.ParsePolicy(s)
	if err != nil {
		return memguard.PolicyTry
	}
	return p
}

// rejectedNote 列出被跳过的档与原因（错误 detail 用）。
func rejectedNote(plan memguard.Plan) string {
	if len(plan.Rejected) == 0 {
		return ""
	}
	parts := make([]string, 0, len(plan.Rejected))
	for _, r := range plan.Rejected {
		parts = append(parts, fmt.Sprintf("%s：%s", r.Name, r.Why))
	}
	return strings.Join(parts, "；")
}

// rejectedSummary 给 stderr 警告用的一句话摘要。
func rejectedSummary(plan memguard.Plan) string {
	if len(plan.Rejected) == 0 {
		return "阈值 " + memguard.HumanSize(plan.Threshold)
	}
	last := plan.Rejected[len(plan.Rejected)-1]
	return fmt.Sprintf("%s 预计 %s > 阈值 %s", last.Name, memguard.HumanSize(last.Predicted), memguard.HumanSize(last.Threshold))
}

// recordSample 把本次执行写进选档历史（失败也要写）。
func recordSample(hist *memguard.History, path string, spec strategy, size, predicted uint64,
	start time.Time, store Store, mem memguard.Memory, ok bool) {
	if hist == nil || path == "" {
		return
	}
	peak := memguard.PeakUsage()
	hist.Add(memguard.Sample{
		Rung: spec.Name, Size: size, Predicted: predicted, Peak: peak, OK: ok,
		MS: time.Since(start).Milliseconds(), Source: mem.Source,
	})
	if err := hist.Save(); err != nil {
		memguard.Debugf("选档历史写入失败：%v", err)
	}
	memguard.Debugf("本次执行档=%s 实测峰值=%s（预计 %s，比值 %.2f，%s，耗时 %dms）",
		spec.Name, memguard.HumanSize(peak), memguard.HumanSize(predicted),
		float64(peak)/float64(max64(predicted, 1)), onOff(ok), time.Since(start).Milliseconds())
}

func max64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

func collect(ctx context.Context, db *sql.DB, sqlText string, max int) (*types.QueryResult, error) {
	rs, err := db.QueryContext(ctx, sqlText)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	cols, err := rs.Columns()
	if err != nil {
		return nil, err
	}
	out := &types.QueryResult{Success: true, Columns: cols, Rows: []types.Row{}}
	for rs.Next() {
		if max > 0 && len(out.Rows) >= max {
			return nil, errMaxRows
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, err
		}
		for i, v := range vals {
			switch x := v.(type) {
			case []byte:
				vals[i] = string(x)
			case float64:
				if math.IsNaN(x) || math.IsInf(x, 0) {
					vals[i] = nil
				}
			}
		}
		out.Rows = append(out.Rows, types.Row{Columns: cols, Values: vals})
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	out.RowCount = len(out.Rows)
	return out, nil
}

const phaseQuery = "query"

// sqliteNOMEM 识别 modernc/sqlite 的分配失败（SQLITE_NOMEM，驱动文本是
// "out of memory (7)"）。它的页缓存是 mmap 出来的，不计入 Go 堆，所以当进程顶到
// 外部硬上限（Windows Job Object、cgroup、ulimit）时会直接返回这个错误——此时
// debug.SetMemoryLimit 帮不上忙，只有 --max-memory 预检与看门狗能提前拦住。
func sqliteNOMEM(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "out of memory") || strings.Contains(msg, "sqlite_nomem")
}

func onOff(b bool) string {
	if b {
		return "开"
	}
	return "关"
}

// modeNotes 把各数据源的装入方式拼成一行（配合 Debugf 使用）。
func modeNotes(modes []LoadMode) string {
	seen := map[LoadMode]bool{}
	var out []string
	for _, m := range modes {
		if !seen[m] {
			seen[m] = true
			out = append(out, string(m))
		}
	}
	return strings.Join(out, "/")
}

// phaseLabel 把内部阶段名翻成用户看得懂的词。
func phaseLabel(phase string) string {
	if phase == "load" {
		return "载入"
	}
	return "查询"
}

func wrapErr(ctx context.Context, o Options, err error, phase string) error {
	var te *types.Error
	switch {
	case errors.As(err, &te):
		return te
	case memguard.PressureError(ctx) != nil:
		return memguard.PressureError(ctx)
	case errors.Is(err, errMaxRows):
		return types.Errorf(types.CodeExec, "result exceeds --max-rows (%d)", o.MaxRows).
			WithHint("在 SQL 中加 LIMIT，或调大 --max-rows")
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return types.Errorf(types.CodeExec, "query timeout after %s", o.Timeout)
	case errors.Is(ctx.Err(), context.Canceled):
		// 信号（Ctrl+C / SIGTERM）打断：与「跑完但出错」区分开——这步没做完、
		// 结果未知，重跑即可，不该让调用方去改输入。
		te := types.Errorf(types.CodeInterrupted, "已中断：%s阶段未完成（收到 Ctrl+C / SIGTERM）", phaseLabel(phase))
		te.Hint = "重跑该命令即可；数据源与 SQL 本身没有问题"
		return te
	}
	if sqliteNOMEM(err) {
		// 把驱动原文（"载入 x.json: out of memory (7)"）翻译成「发生了什么 + 怎么办」。
		te = types.Errorf(types.CodeExec, "内存不足：%s阶段 SQLite 分配失败", phaseLabel(phase))
		te.Detail = err.Error() + "（进程顶到了外部内存上限，Go 堆软上限管不到 mmap 出去的页缓存）"
		te.Hint = "缩小输入或改用 --load-mode stream；若是被容器/Job Object/ulimit 限制，用 --max-memory 显式声明预算让预检提前拦住（Windows 会自动识别 Job Object，macOS 需显式指定）"
		return te
	}
	te = types.Errorf(types.CodeExec, "%s", err.Error())
	if strings.Contains(err.Error(), "no such table") {
		te.Hint = "表名请用 --source 别名=引用 绑定，或把文件路径用双引号包裹（单引号无效）"
	}
	return te
}

var (
	quotedRe     = regexp.MustCompile("\"([^\"]+)\"|`([^`]+)`")
	fromQuotedRe = regexp.MustCompile("(?i)\\b(?:from|join)\\s+(?:\"([^\"]+)\"|`([^`]+)`)")
	fromBareRe   = regexp.MustCompile("(?i)\\b(?:from|join)\\s+([^\\s,()\"'`;]+)")
	literalRe    = regexp.MustCompile(`'(?:[^']|'')*'`)
)

type edit struct {
	start, end int
	text       string
}

// Rewrite 校验（沙箱）并把别名/文件路径改写为内存库中的表名，返回需要载入的文件。
func Rewrite(o Options) (string, []Binding, error) {
	sqlText := strings.TrimSpace(o.SQL)
	if sqlText == "" {
		return "", nil, types.Errorf(types.CodeUsage, "empty SQL")
	}
	if o.Sandbox {
		if err := checkStatement(sqlText); err != nil {
			return "", nil, err
		}
	}
	var edits []edit
	var binds []Binding
	byName := map[string]bool{}
	bind := func(name, path string) string {
		if !byName[name] {
			byName[name] = true
			binds = append(binds, Binding{Name: name, Path: path})
		}
		return ident(name)
	}
	byPath := map[string]string{}
	handle := func(start, end int, name string, inFrom bool) error {
		if p, ok := o.Sources[name]; ok {
			edits = append(edits, edit{start, end, bind(name, p)})
			return nil
		}
		if inFrom && o.Lookup != nil {
			if p, ok := o.Lookup(name); ok {
				edits = append(edits, edit{start, end, bind(name, p)})
				return nil
			}
		}
		p, ok := resolveFile(name, o.Roots)
		if !ok {
			return nil
		}
		if o.Sandbox && !within(p, o.Roots) {
			return types.Errorf(types.CodeExec, "sandbox violation: %s", name).
				WithDetail("SQL 只能读取工作区、当前目录或 --source 显式绑定的文件")
		}
		tn, ok := byPath[p]
		if !ok {
			tn = fmt.Sprintf("__src%d", len(byPath)+1)
			byPath[p] = tn
		}
		edits = append(edits, edit{start, end, bind(tn, p)})
		return nil
	}
	fromQuoted := map[int]bool{}
	for _, m := range fromQuotedRe.FindAllStringSubmatchIndex(sqlText, -1) {
		g := m[2]
		if g < 0 {
			g = m[4]
		}
		fromQuoted[g-1] = true // 引号起点
	}
	for _, m := range quotedRe.FindAllStringSubmatchIndex(sqlText, -1) {
		name := ""
		if m[2] >= 0 {
			name = sqlText[m[2]:m[3]]
		} else {
			name = sqlText[m[4]:m[5]]
		}
		if err := handle(m[0], m[1], name, fromQuoted[m[0]]); err != nil {
			return "", nil, err
		}
	}
	for _, m := range fromBareRe.FindAllStringSubmatchIndex(sqlText, -1) {
		if err := handle(m[2], m[3], sqlText[m[2]:m[3]], true); err != nil {
			return "", nil, err
		}
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	for _, e := range edits {
		sqlText = sqlText[:e.start] + e.text + sqlText[e.end:]
	}
	return sqlText, binds, nil
}

// checkStatement 沙箱：只允许单条 SELECT/WITH（内存库不会读文件，但需挡住 ATTACH/PRAGMA 等）。
func checkStatement(sqlText string) error {
	stripped := literalRe.ReplaceAllString(sqlText, "''")
	t := strings.TrimRight(strings.TrimSpace(stripped), "; \t\r\n")
	up := strings.ToUpper(t)
	if !strings.HasPrefix(up, "SELECT") && !strings.HasPrefix(up, "WITH") {
		return types.Errorf(types.CodeExec, "sandbox violation: only SELECT/WITH statements are allowed")
	}
	if strings.Contains(quotedRe.ReplaceAllString(t, `""`), ";") {
		return types.Errorf(types.CodeExec, "sandbox violation: multiple statements are not allowed")
	}
	return nil
}

func resolveFile(name string, roots []string) (string, bool) {
	var cands []string
	if filepath.IsAbs(name) {
		cands = []string{name}
	} else {
		for _, r := range roots {
			cands = append(cands, filepath.Join(r, name), filepath.Join(r, "outputs", name))
		}
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && st.Mode().IsRegular() {
			if real, err := filepath.EvalSymlinks(c); err == nil {
				return real, true
			}
			return c, true
		}
	}
	return "", false
}

func within(p string, roots []string) bool {
	for _, r := range roots {
		real, err := filepath.EvalSymlinks(r)
		if err != nil {
			real = r
		}
		rel, err := filepath.Rel(real, p)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// joinReason 拼接选档理由，空串不加分隔符。
func joinReason(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "；" + b
}
