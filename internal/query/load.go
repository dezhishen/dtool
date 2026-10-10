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
		WithHint("可选：auto（按内存预算在 full/stream/磁盘档之间选）/ stream（流式，省内存）/ full（整块解析，快，要求更多余量）")
}

// Preview 是「不真正加载」的装入预演：每档预计多少、会不会被选中。
type Preview struct {
	Alias     string  `json:"alias"`
	Path      string  `json:"path"`
	Size      uint64  `json:"size"`
	SizeHuman string  `json:"size_human"`
	Mode      string  `json:"mode"`  // 装入方式
	Store     string  `json:"store"` // 落库位置
	Need      uint64  `json:"need"`  // 预计峰值（Base + size×Factor）
	NeedHuman string  `json:"need_human"`
	Factor    float64 `json:"factor"` // 估算倍率
	Chosen    bool    `json:"chosen"` // 本次会被选中的档
	Chance    float64 `json:"chance"` // 按历史估计这一档能过的概率
	Note      string  `json:"note,omitempty"`
}

// LadderPreview 是阶梯预演结果：每档的数字 + 最终选择 + 理由。
type LadderPreview struct {
	Sources   []Preview     `json:"sources"`
	Rungs     []RungPreview `json:"rungs"`
	Chosen    string        `json:"chosen"`
	Reason    string        `json:"reason"`
	Chance    float64       `json:"chance"`
	Predicted uint64        `json:"predicted"`
	Threshold uint64        `json:"threshold"`
	Risky     bool          `json:"risky"`
	Forced    bool          `json:"forced"`
	Verdict   string        `json:"verdict"` // ok / borderline / risky / forced
}

// RungPreview 是单个执行档的预演数据。
// RungPreview 是单个执行档的预演数据。Need 是**按历史校准后**的预计峰值；
// RawNeed 是按倍率直接算出来的值，Calibration 是两者之比（>1 说明这一档历史上
// 实测超过估算，例如 Windows 小输入在内存档上实测到估算的 3 倍）。
type RungPreview struct {
	Name         string  `json:"name"`
	Note         string  `json:"note"`
	Need         uint64  `json:"need"`
	NeedHuman    string  `json:"need_human"`
	RawNeed      uint64  `json:"raw_need,omitempty"`
	RawNeedHuman string  `json:"raw_need_human,omitempty"`
	Calibration  float64 `json:"calibration,omitempty"`
	CalibSample  int     `json:"calibration_samples,omitempty"`
	Chance       float64 `json:"chance"`
	Chosen       bool    `json:"chosen"`
}

// PreviewPlan 按预算与历史预演整条阶梯（不读文件内容），供 meminfo 使用。
// 它回答的是「这条命令会选哪一档、为什么」，而不是「会不会被拒绝」——现在被拒绝
// 只是「连最省档都预计放不下」时的一种可选策略（--mem-policy strict）。
func PreviewPlan(loadMode, store, memPolicy string, mem memguard.Memory, planFile string,
	srcs map[string]string) (LadderPreview, error) {
	return previewPlan(loadMode, store, memPolicy, mem, planFile, srcs)
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
