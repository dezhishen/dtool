package converter

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

type ColumnSchema struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	SQLType  string   `json:"sql_type"`
	Nullable bool     `json:"nullable"`
	Unique   bool     `json:"unique,omitempty"`
	Enum     []string `json:"enum,omitempty"`
	Min      *float64 `json:"min,omitempty"`
	Max      *float64 `json:"max,omitempty"`
	Format   string   `json:"format,omitempty"`
	Samples  []any    `json:"samples,omitempty"`
}

const (
	enumLimit   = 20
	uniqueLimit = 100000
	sampleCount = 3
)

var (
	intRe    = regexp.MustCompile(`^-?\d+$`)
	floatRe  = regexp.MustCompile(`^[-+]?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?$`)
	leadZero = regexp.MustCompile(`^[-+]?0\d`)
)

// 日期布局：输入布局 -> 输出布局。
var dateLayouts = []struct{ in, out string }{
	{"2006-01-02", "2006-01-02"},
	{"2006/01/02", "2006-01-02"},
	{"2006/1/2", "2006-01-02"},
	{"2006-01-02 15:04:05", "2006-01-02T15:04:05"},
	{"2006/01/02 15:04:05", "2006-01-02T15:04:05"},
	{"2006-01-02T15:04:05", "2006-01-02T15:04:05"},
	{time.RFC3339, time.RFC3339},
}

func sqlType(t string) string {
	switch t {
	case "integer":
		return "INTEGER"
	case "number":
		return "REAL"
	case "boolean":
		return "BOOLEAN"
	}
	return "TEXT"
}

func isBlank(s string) bool { return strings.TrimSpace(s) == "" }

// detectType 仅基于整列非空值判断类型；前导零数字串与超长整数保持 string。
func detectType(vals []string) (typ, format string) {
	if len(vals) == 0 {
		return "null", ""
	}
	all := func(f func(string) bool) bool {
		for _, v := range vals {
			if !f(strings.TrimSpace(v)) {
				return false
			}
		}
		return true
	}
	if all(func(s string) bool {
		return intRe.MatchString(s) && len(strings.TrimLeft(s, "-")) <= 15 && !leadZero.MatchString(s)
	}) {
		return "integer", ""
	}
	if all(func(s string) bool { return floatRe.MatchString(s) && !leadZero.MatchString(s) && len(s) <= 30 }) {
		return "number", ""
	}
	if all(func(s string) bool { l := strings.ToLower(s); return l == "true" || l == "false" }) {
		return "boolean", ""
	}
	for _, l := range dateLayouts {
		l := l
		if all(func(s string) bool { _, err := time.Parse(l.in, s); return err == nil }) {
			return "date", l.out
		}
	}
	return "string", ""
}

func convertCell(typ, s string) any {
	if isBlank(s) {
		return nil
	}
	t := strings.TrimSpace(s)
	switch typ {
	case "integer":
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	case "number":
		f, _ := strconv.ParseFloat(t, 64)
		return f
	case "boolean":
		return strings.ToLower(t) == "true"
	case "date":
		for _, l := range dateLayouts {
			if tm, err := time.Parse(l.in, t); err == nil {
				return tm.Format(l.out)
			}
		}
	}
	return s
}

// InferSchema 从按列整理的原始字符串推断每列 Schema。cols[i] 为第 i 列所有行（含空串）。
func InferSchema(headers []string, cols [][]string, records int) []ColumnSchema {
	out := make([]ColumnSchema, len(headers))
	for i, h := range headers {
		var nonNull []string
		for _, v := range cols[i] {
			if !isBlank(v) {
				nonNull = append(nonNull, v)
			}
		}
		typ, format := detectType(nonNull)
		cs := ColumnSchema{Name: h, Type: typ, SQLType: sqlType(typ), Format: format,
			Nullable: len(nonNull) < records}

		seen := map[string]struct{}{}
		overflow := false
		var order []string
		for _, v := range nonNull {
			if _, ok := seen[v]; ok {
				continue
			}
			if len(seen) >= uniqueLimit {
				overflow = true
				break
			}
			seen[v] = struct{}{}
			if len(order) < enumLimit+1 {
				order = append(order, v)
			}
		}
		if !overflow {
			cs.Unique = len(nonNull) == records && len(seen) == records && records > 0
			if typ == "string" && len(seen) <= enumLimit && len(seen) < len(nonNull) {
				cs.Enum = order
			}
		}
		for _, v := range order {
			if len(cs.Samples) >= sampleCount {
				break
			}
			cs.Samples = append(cs.Samples, convertCell(typ, v))
		}
		if typ == "integer" || typ == "number" {
			for _, v := range nonNull {
				f, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
				if cs.Min == nil || f < *cs.Min {
					m := f
					cs.Min = &m
				}
				if cs.Max == nil || f > *cs.Max {
					m := f
					cs.Max = &m
				}
			}
		}
		out[i] = cs
	}
	return out
}
