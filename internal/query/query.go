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
	"sort"
	"strings"
	"time"

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
	Lookup  func(name string) (path string, ok bool) // 数据集名 -> 数据文件，可为 nil
}

var errMaxRows = errors.New("max rows exceeded")

// Run 重写表引用，把用到的 JSON 文件载入内存 SQLite（modernc，纯 Go）后执行查询。
func Run(ctx context.Context, o Options) (*types.QueryResult, error) {
	sqlText, binds, err := Rewrite(o)
	if err != nil {
		return nil, err
	}
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // 内存库按连接隔离
	for _, b := range binds {
		if err := loadTable(ctx, db, b); err != nil {
			return nil, wrapErr(ctx, o, err)
		}
	}
	res, err := collect(ctx, db, sqlText, o.MaxRows)
	if err != nil {
		return nil, wrapErr(ctx, o, err)
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

func wrapErr(ctx context.Context, o Options, err error) error {
	var te *types.Error
	switch {
	case errors.As(err, &te):
		return te
	case errors.Is(err, errMaxRows):
		return types.Errorf(types.CodeExec, "result exceeds --max-rows (%d)", o.MaxRows).
			WithHint("在 SQL 中加 LIMIT，或调大 --max-rows")
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return types.Errorf(types.CodeExec, "query timeout after %s", o.Timeout)
	case errors.Is(ctx.Err(), context.Canceled):
		return types.Errorf(types.CodeExec, "query canceled")
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
