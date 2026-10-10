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
	// LoadMode 决定 JSON → SQLite 的装入方式："" 或 auto 按文件大小自适应，stream 流式，full 整块解析。
	LoadMode string
	Lookup   func(name string) (path string, ok bool) // 数据集名 -> 数据文件，可为 nil
}

var errMaxRows = errors.New("max rows exceeded")

// peakFactor 为各装入方式的实测峰值倍率（见 README）：
// full 整块解析约 13 倍文件大小；stream 逐行装入只需 ~1.3 倍，取 2 倍留余量。
const (
	fullPeakFactor   = 13
	streamPeakFactor = 2
	// autoStreamSize：auto 模式下达到该体积即改用流式。
	autoStreamSize = 32 << 20
)

// progressMinSize 可在测试中替换。
var progressMinSize = uint64(memguard.DefaultProgressMinSize)

// Run 重写表引用，把用到的 JSON 文件载入内存 SQLite（modernc，纯 Go）后执行查询。
// 载入前会按可用内存做预估，载入/查询期间有内存看门狗，超限时以普通错误退出并给出原因。
func Run(ctx context.Context, o Options) (*types.QueryResult, error) {
	sqlText, binds, err := Rewrite(o)
	if err != nil {
		return nil, err
	}
	// 载入前先估算内存：内存不足时给出可读的原因，而不是被内核 OOM 直接杀掉
	mem := memguard.Budget(o.MaxMemory)
	modes := make([]LoadMode, len(binds))
	var total, need uint64
	for i, b := range binds {
		size := memguard.SizeOf(b.Path)
		modes[i] = resolveMode(o.LoadMode, size, mem)
		total += size
		need += size * uint64(peakFactor(modes[i]))
	}
	if err := memguard.CheckNeed("数据源", total, need, estimateNote(modes), mem, memguard.HintLoadMode); err != nil {
		return nil, err
	}
	if mem.Available > 0 {
		for i, m := range modes {
			if m == LoadFull && fullTight(memguard.SizeOf(binds[i].Path), mem) {
				fmt.Fprintf(memguard.NoticeWriter,
					"提示：%s 走整块解析，预计峰值已逼近预算（可用 %s），中途可能被中止；改用 --load-mode stream 更稳。\n",
					filepath.Base(binds[i].Path), memguard.HumanSize(mem.Available))
			}
		}
		// 软上限：接近预算时 GC 更积极，尽量不撞上 cgroup 硬限制
		defer debug.SetMemoryLimit(-1)
		debug.SetMemoryLimit(int64(mem.Available))
	}
	ctx, stopWatch := memguard.Watch(ctx, mem.Available)
	defer stopWatch()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // 内存库按连接隔离
	for i, b := range binds {
		size := memguard.SizeOf(b.Path)
		memguard.Progress(os.Stderr, progressMinSize, filepath.Base(b.Path), size, size*uint64(peakFactor(modes[i])), modeNote(modes[i]))
		if err := loadTable(ctx, db, b, modes[i]); err != nil {
			return nil, wrapErr(ctx, o, fmt.Errorf("载入 %s: %w", filepath.Base(b.Path), err), "load")
		}
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
	return res, nil
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
