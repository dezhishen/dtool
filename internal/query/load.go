package query

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/dezhishen/dtool/pkg/types"
)

// Binding 把 SQL 中的表名绑定到一个 JSON 数组文件。
type Binding struct {
	Name string
	Path string
}

func ident(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// 列类型按整列推断：全整数 INTEGER，全数值 REAL，其余 TEXT（避免首行为空或混合类型导致比较/排序错误）。
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

// loadTable 把 JSON 数组文件整体载入内存库的一张表。
func loadTable(ctx context.Context, db *sql.DB, b Binding) error {
	data, err := os.ReadFile(b.Path)
	if err != nil {
		return types.Errorf(types.CodeNotFound, "source not found: %s", b.Path)
	}
	var rows []types.Row
	if err := json.Unmarshal(data, &rows); err != nil {
		return types.Errorf(types.CodeExec, "source %s is not a JSON array of objects: %v", b.Name, err)
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

	defs := make([]string, len(cols))
	marks := make([]string, len(cols))
	for i, c := range cols {
		defs[i] = ident(c) + " " + ctypes[i]
		marks[i] = "?"
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s (%s)", ident(b.Name), strings.Join(defs, ", "))); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, fmt.Sprintf("INSERT INTO %s VALUES (%s)", ident(b.Name), strings.Join(marks, ",")))
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
