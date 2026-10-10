package query

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/dezhishen/dtool/internal/memguard"
	"github.com/dezhishen/dtool/pkg/types"
)

// Binding 把 SQL 中的表名绑定到一个 JSON 数组文件。
type Binding struct {
	Name string
	Path string
}

// LoadMode 决定 JSON 装入内存 SQLite 的方式。
type LoadMode string

const (
	LoadAuto   LoadMode = "auto"
	LoadStream LoadMode = "stream"
	LoadFull   LoadMode = "full"
)

// ParseLoadMode 解析 --load-mode；空串按 auto 处理。
func ParseLoadMode(s string) (LoadMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return LoadAuto, nil
	case "stream":
		return LoadStream, nil
	case "full":
		return LoadFull, nil
	}
	return "", types.Errorf(types.CodeUsage, "invalid load mode %q", s).
		WithHint("可选：auto（按文件大小自适应）/ stream（流式，省内存）/ full（整块解析，快）")
}

// autoFullHeadroom 是 auto 模式选择整块解析时要求的最小余量（百分比）。
// 估算倍率来自实测量级，擦着预算通过往往意味着加载中途被看门狗中止——宁可慢一点也走流式。
const autoFullHeadroom = 80

// resolveMode 决定单个数据源的装入方式：显式指定优先；auto 时文件够大、或整块解析放不下就改用流式。
func resolveMode(mode string, size uint64, mem memguard.Memory) LoadMode {
	if m, err := ParseLoadMode(mode); err == nil && m != LoadAuto {
		return m
	}
	if size >= autoStreamSize {
		return LoadStream
	}
	if mem.Available > 0 && size*fullPeakFactor > mem.Available*autoFullHeadroom/100 {
		return LoadStream
	}
	return LoadFull
}

// Preview 是「不真正加载」的装入预演结果：回答「按当前预算，这条命令会不会被预检拦下」。
type Preview struct {
	Alias     string `json:"alias"`
	Path      string `json:"path"`
	Size      uint64 `json:"size"`
	SizeHuman string `json:"size_human"`
	Mode      string `json:"mode"`
	Need      uint64 `json:"need"`
	NeedHuman string `json:"need_human"`
	Verdict   string `json:"verdict"`
	Error     string `json:"error,omitempty"`
	ErrorHint string `json:"error_hint,omitempty"`
}

// PreviewLoad 按体积与装入方式预演一组数据源（不读内容），供 meminfo 使用。
func PreviewLoad(loadMode string, mem memguard.Memory, srcs map[string]string) []Preview {
	aliases := make([]string, 0, len(srcs))
	for a := range srcs {
		aliases = append(aliases, a)
	}
	sort.Strings(aliases)
	out := make([]Preview, 0, len(aliases))
	var total, need uint64
	modes := make([]LoadMode, 0, len(aliases))
	for _, a := range aliases {
		size := memguard.SizeOf(srcs[a])
		m := resolveMode(loadMode, size, mem)
		modes = append(modes, m)
		total += size
		need += size * uint64(peakFactor(m))
		out = append(out, Preview{Alias: a, Path: srcs[a], Size: size,
			SizeHuman: memguard.HumanSize(size), Mode: string(m),
			Need: size * uint64(peakFactor(m)), NeedHuman: memguard.HumanSize(size * uint64(peakFactor(m)))})
	}
	err := memguard.CheckNeed("数据源", total, need, estimateNote(modes), mem, memguard.HintLoadMode)
	for i := range out {
		if err == nil {
			out[i].Verdict = "ok"
			continue
		}
		out[i].Verdict = "refused"
		out[i].Error = err.Error()
		if te, ok := err.(*types.Error); ok {
			out[i].ErrorHint = te.Hint
		}
	}
	return out
}

// fullTight 判断整块解析的预计峰值是否逼近预算（超过 headroom 比例）。
// 显式 --load-mode full 时用来提醒用户：这次很可能在中途被中止。
func fullTight(size uint64, mem memguard.Memory) bool {
	if mem.Available == 0 || size == 0 {
		return false
	}
	need := size * fullPeakFactor
	return need <= mem.Available && need > mem.Available*autoFullHeadroom/100
}

func peakFactor(m LoadMode) int {
	if m == LoadStream {
		return streamPeakFactor
	}
	return fullPeakFactor
}

func modeNote(m LoadMode) string {
	if m == LoadStream {
		return "流式解析"
	}
	return "整块解析"
}

func estimateNote(modes []LoadMode) string {
	for _, m := range modes {
		if m == LoadStream {
			return "含流式装入，按整块 13 倍 / 流式 2 倍估算"
		}
	}
	return fmt.Sprintf("按整块解析实测 %d 倍估算", fullPeakFactor)
}

func ident(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// loadTable 按模式把 JSON 数组文件载入内存库的一张表。
func loadTable(ctx context.Context, db *sql.DB, b Binding, mode LoadMode) error {
	if mode == LoadStream {
		return loadStream(ctx, db, b)
	}
	return loadFull(ctx, db, b)
}

// rowKV 保持 JSON 对象内的键顺序（map 会丢顺序，列顺序必须稳定）。
type rowKV struct {
	key string
	val any
}

// streamArray 逐行流式读取 JSON 数组文件，每行回调一次，内存占用与文件大小无关。
func streamArray(ctx context.Context, path string, fn func(n int, row []rowKV) error) error {
	f, err := os.Open(path)
	if err != nil {
		return types.Errorf(types.CodeNotFound, "source not found: %s", path)
	}
	defer f.Close()
	dec := json.NewDecoder(bufio.NewReaderSize(f, 1<<20))
	dec.UseNumber()
	if tok, err := dec.Token(); err != nil {
		return types.Errorf(types.CodeExec, "source %s is not a JSON array: %v", path, err)
	} else if tok != json.Delim('[') {
		return types.Errorf(types.CodeExec, "source %s is not a JSON array of objects", path)
	}
	for n := 0; dec.More(); n++ {
		if err := ctx.Err(); err != nil {
			return err // 让 –-timeout 与内存看门狗能中断载入
		}
		tok, err := dec.Token()
		if err != nil {
			return types.Errorf(types.CodeExec, "source %s: row %d: %v", path, n+1, err)
		}
		if tok != json.Delim('{') {
			return types.Errorf(types.CodeExec, "source %s: row %d is not a JSON object", path, n+1)
		}
		var row []rowKV
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return types.Errorf(types.CodeExec, "source %s: row %d: %v", path, n+1, err)
			}
			key, ok := kt.(string)
			if !ok {
				return types.Errorf(types.CodeExec, "source %s: row %d: invalid key", path, n+1)
			}
			var v any
			if err := dec.Decode(&v); err != nil {
				return types.Errorf(types.CodeExec, "source %s: row %d: %v", path, n+1, err)
			}
			row = append(row, rowKV{key, v})
		}
		if _, err := dec.Token(); err != nil { // '}'
			return types.Errorf(types.CodeExec, "source %s: row %d: %v", path, n+1, err)
		}
		if err := fn(n, row); err != nil {
			return err
		}
	}
	// dec.More 不会消费结尾的 ']'，这里补上
	if tok, err := dec.Token(); err != nil {
		return types.Errorf(types.CodeExec, "source %s: %v", path, err)
	} else if tok != json.Delim(']') {
		return types.Errorf(types.CodeExec, "source %s: invalid JSON array", path)
	}
	// 数组之后不允许再有内容，避免把损坏的文件当成正常数据
	if _, err := dec.Token(); err != io.EOF {
		return types.Errorf(types.CodeExec, "source %s: unexpected trailing content", path)
	}
	return nil
}

// colStat 逐行累积列类型：全整数 INTEGER，全数值 REAL，其余 TEXT（与整块解析口径一致）。
type colStat struct {
	name         string
	isInt, isNum bool
	seen         bool
}

func (c *colStat) observe(v any) {
	switch x := v.(type) {
	case nil:
		return
	case json.Number:
		c.seen = true
		if _, err := strconv.ParseInt(string(x), 10, 64); err != nil {
			c.isInt = false
		}
		if _, err := strconv.ParseFloat(string(x), 64); err != nil {
			c.isNum = false
		}
	case bool:
		// 与整块解析口径一致：布尔列落成 INTEGER 的 0/1
		c.seen, c.isNum = true, false
	default:
		c.seen, c.isInt, c.isNum = true, false, false
	}
}

func (c *colStat) sqlType() string {
	switch {
	case c.seen && c.isInt:
		return "INTEGER"
	case c.seen && c.isNum:
		return "REAL"
	default:
		return "TEXT"
	}
}

// loadStream 两遍流式载入：第一遍推断列与类型，第二遍写入，峰值内存与文件大小基本无关。
func loadStream(ctx context.Context, db *sql.DB, b Binding) error {
	var cols []string
	stats := map[string]*colStat{}
	err := streamArray(ctx, b.Path, func(_ int, row []rowKV) error {
		for _, kv := range row {
			st := stats[kv.key]
			if st == nil {
				st = &colStat{name: kv.key, isInt: true, isNum: true}
				stats[kv.key] = st
				cols = append(cols, kv.key)
			}
			st.observe(kv.val)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		cols = []string{"_empty"}
	}
	ctypes := make([]string, len(cols))
	idx := make(map[string]int, len(cols))
	for i, c := range cols {
		ctypes[i] = stats[c].sqlType()
		idx[c] = i
	}
	if err := createTable(ctx, db, b.Name, cols, ctypes); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, insertSQL(b.Name, len(cols)))
	if err != nil {
		return err
	}
	defer stmt.Close()
	args := make([]any, len(cols))
	if err := streamArray(ctx, b.Path, func(_ int, row []rowKV) error {
		for i := range args {
			args[i] = nil
		}
		for _, kv := range row {
			if i, ok := idx[kv.key]; ok {
				args[i] = sqlValue(kv.val, ctypes[i])
			}
		}
		_, err := stmt.ExecContext(ctx, args...)
		return err
	}); err != nil {
		return err
	}
	return tx.Commit()
}

func insertSQL(name string, n int) string {
	marks := make([]string, n)
	for i := range marks {
		marks[i] = "?"
	}
	return fmt.Sprintf("INSERT INTO %s VALUES (%s)", ident(name), strings.Join(marks, ","))
}

func createTable(ctx context.Context, db *sql.DB, name string, cols, ctypes []string) error {
	defs := make([]string, len(cols))
	for i, c := range cols {
		defs[i] = ident(c) + " " + ctypes[i]
	}
	_, err := db.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s (%s)", ident(name), strings.Join(defs, ", ")))
	return err
}

// columnTypes 整块解析用：按整列推断类型。
func columnTypes(cols []string, rows []types.Row) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		isInt, isNum, seen := true, true, false
		for _, r := range rows {
			v, ok := r.Get(c)
			if !ok || v == nil {
				continue
			}
			seen = true
			n, isNumber := v.(json.Number)
			if !isNumber {
				if _, isBool := v.(bool); isBool {
					isNum = false
				} else {
					isInt, isNum = false, false
				}
				continue
			}
			if _, err := strconv.ParseInt(string(n), 10, 64); err != nil {
				isInt = false
			}
			if _, err := strconv.ParseFloat(string(n), 64); err != nil {
				isNum = false
			}
		}
		switch {
		case seen && isInt:
			out[i] = "INTEGER"
		case seen && isNum:
			out[i] = "REAL"
		default:
			out[i] = "TEXT"
		}
	}
	return out
}

func sqlValue(v any, typ string) any {
	switch x := v.(type) {
	case nil:
		return nil
	case json.Number:
		switch typ {
		case "INTEGER":
			n, _ := strconv.ParseInt(string(x), 10, 64)
			return n
		case "REAL":
			f, _ := strconv.ParseFloat(string(x), 64)
			return f
		}
		return string(x)
	case bool:
		if typ == "TEXT" {
			return strconv.FormatBool(x)
		}
		return boolInt(x)
	case string:
		return x
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// decodeRows 为整块解析逐元素解码 JSON 数组。
//
// 内存特点与 json.Unmarshal 相同（所有行都驻留在内存里），但两点更好：
//   - 每 4096 行检查一次 ctx，看门狗判定内存超预算时能及时中止，而不是等整个
//     Unmarshal 跑完（GC 抖动下这可能要几十秒且毫无输出）；
//   - 不再同时持有「文件原始字节 + 解码后的行」两份，峰值少一个文件大小。
func decodeRows(ctx context.Context, r io.Reader, name string) ([]types.Row, error) {
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil {
		return nil, types.Errorf(types.CodeExec, "source %s is not a JSON array of objects: %v", name, err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return nil, types.Errorf(types.CodeExec, "source %s is not a JSON array of objects: 应以 [ 开头", name)
	}
	var rows []types.Row
	for i := 0; dec.More(); i++ {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		var row types.Row
		if err := dec.Decode(&row); err != nil {
			return nil, types.Errorf(types.CodeExec, "source %s is not a JSON array of objects: %v", name, err)
		}
		rows = append(rows, row)
	}
	// 先消费数组的 ']'，再看后面还有没有内容：与 json.Unmarshal 一样严格，
	// 避免把 `[...] [...]` 这种文件当成合法输入悄悄读进来。
	if tok, err := dec.Token(); err != nil {
		return nil, types.Errorf(types.CodeExec, "source %s is not a JSON array of objects: %v", name, err)
	} else if d, ok := tok.(json.Delim); !ok || d != ']' {
		return nil, types.Errorf(types.CodeExec, "source %s is not a JSON array of objects: 数组未正常结束", name)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, types.Errorf(types.CodeExec, "source %s is not a JSON array of objects: 数组结尾后有多余内容", name)
		}
		return nil, types.Errorf(types.CodeExec, "source %s is not a JSON array of objects: %v", name, err)
	}
	return rows, nil
}

// loadFull 整块解析：读入整个文件并反序列化为行切片后再插入（快，但峰值内存约 13 倍文件大小）。
func loadFull(ctx context.Context, db *sql.DB, b Binding) error {
	f, err := os.Open(b.Path)
	if err != nil {
		return types.Errorf(types.CodeNotFound, "source not found: %s", b.Path)
	}
	defer f.Close()
	rows, err := decodeRows(ctx, bufio.NewReaderSize(f, 1<<20), b.Name)
	if err != nil {
		return err
	}
	var cols []string
	seen := map[string]bool{}
	for _, r := range rows {
		for _, c := range r.Columns {
			if !seen[c] {
				seen[c] = true
				cols = append(cols, c)
			}
		}
	}
	if len(cols) == 0 {
		cols = []string{"_empty"}
	}
	ctypes := columnTypes(cols, rows)
	if err := createTable(ctx, db, b.Name, cols, ctypes); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, insertSQL(b.Name, len(cols)))
	if err != nil {
		return err
	}
	defer stmt.Close()
	args := make([]any, len(cols))
	for _, r := range rows {
		for i, c := range cols {
			v, _ := r.Get(c)
			args[i] = sqlValue(v, ctypes[i])
		}
		if _, err := stmt.ExecContext(ctx, args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}
