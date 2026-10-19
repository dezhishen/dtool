package converter

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ColumnSchema 只描述结构，不包含任何原始取值（枚举/样例/极值都不在这里）。
// 需要具体值时走 `dtool datasets values` 这一显式数据动作。
type ColumnSchema struct {
	Name              string `json:"name"`
	Type              string `json:"type"`
	SQLType           string `json:"sql_type"`
	Nullable          bool   `json:"nullable"`
	Unique            bool   `json:"unique,omitempty"`
	DistinctCount     int    `json:"distinct_count,omitempty"`
	DistinctTruncated bool   `json:"distinct_count_truncated,omitempty"`
	Role              string `json:"role,omitempty"`
	Format            string `json:"format,omitempty"`
}

const uniqueLimit = 100000

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

// colStat 逐行累积单列的统计信息；InferSchema 与流式转换共用它，
// 保证「整列推断」的规则只有一份实现。
type colStat struct {
	nonNull int
	seen    map[string]struct{}
	over    bool // 不同值超过 uniqueLimit，此时不再跟踪 seen

	intOK  bool // 迄今所有非空值都满足整数规则（含 ≤15 位、无前导零）
	numOK  bool // ... 都满足数字规则
	boolOK bool // ... 都是 true/false
	dateOK []bool
}

func newColStat() *colStat {
	c := &colStat{seen: map[string]struct{}{}, intOK: true, numOK: true, boolOK: true}
	c.dateOK = make([]bool, len(dateLayouts))
	for i := range c.dateOK {
		c.dateOK[i] = true
	}
	return c
}

// observe 记录一个原始单元格值；空值不计入非空统计，也不影响类型判定。
func (c *colStat) observe(raw string) {
	if isBlank(raw) {
		return
	}
	c.nonNull++
	v := strings.TrimSpace(raw)

	if !c.over {
		if _, ok := c.seen[v]; !ok {
			if len(c.seen) >= uniqueLimit {
				c.over = true // 溢出后不再收集；unique/distinct_count 标记为不可知
			} else {
				c.seen[v] = struct{}{}
			}
		}
	}

	if c.intOK && !(intRe.MatchString(v) && len(strings.TrimLeft(v, "-")) <= 15 && !leadZero.MatchString(v)) {
		c.intOK = false
	}
	if c.numOK && !(floatRe.MatchString(v) && !leadZero.MatchString(v) && len(v) <= 30) {
		c.numOK = false
	}
	if c.boolOK {
		if l := strings.ToLower(v); l != "true" && l != "false" {
			c.boolOK = false
		}
	}
	for i := range c.dateOK {
		if c.dateOK[i] {
			if _, err := time.Parse(dateLayouts[i].in, v); err != nil {
				c.dateOK[i] = false
			}
		}
	}
}

// typ 按「整数 → 数字 → 布尔 → 日期 → 文本」的顺序判定，要求全部非空值都满足。
func (c *colStat) typ() (typ, format string) {
	switch {
	case c.nonNull == 0:
		return "null", ""
	case c.intOK:
		return "integer", ""
	case c.numOK:
		return "number", ""
	case c.boolOK:
		return "boolean", ""
	}
	for i, l := range dateLayouts {
		if c.dateOK[i] {
			return "date", l.out
		}
	}
	return "string", ""
}

// column 依据累积结果产出列 Schema（只含结构，不含原始取值）。
func (c *colStat) column(name string, records int) ColumnSchema {
	typ, format := c.typ()
	cs := ColumnSchema{Name: name, Type: typ, SQLType: sqlType(typ), Format: format,
		Nullable: c.nonNull < records}
	cs.DistinctCount = len(c.seen)
	cs.DistinctTruncated = c.over
	if !c.over {
		cs.Unique = c.nonNull == records && len(c.seen) == records && records > 0
	}
	cs.Role = roleOf(typ, cs.Unique, cs.DistinctCount, c.nonNull)
	return cs
}

// roleOf 给出列的语义角色，帮助 AI 决定能否 GROUP BY/筛选，但不泄露任何值。
func roleOf(typ string, unique bool, distinct, nonNull int) string {
	if unique {
		return "identifier"
	}
	switch typ {
	case "date":
		return "time"
	case "integer", "number":
		return "measure"
	case "boolean":
		return "flag"
	case "string":
		// 每个值平均至少重复 3 次，且基数不大，才视为分类字段。
		if distinct > 0 && distinct <= 50 && nonNull >= distinct*3 {
			return "category"
		}
		return "text"
	}
	return "text"
}

// convertCell 把原始单元格按推断出的类型转成 JSON 值；用于写 data.json，不用于 Schema 展示。
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

// detectType 仅基于整列非空值判断类型；保留它是为了单测能直接断言类型规则。
func detectType(vals []string) (typ, format string) {
	st := newColStat()
	for _, v := range vals {
		st.observe(v)
	}
	return st.typ()
}

// InferSchema 从按列整理的原始字符串推断每列 Schema。cols[i] 为第 i 列所有行（含空串）。
func InferSchema(headers []string, cols [][]string, records int) []ColumnSchema {
	out := make([]ColumnSchema, len(headers))
	for i, h := range headers {
		st := newColStat()
		for _, v := range cols[i] {
			st.observe(v)
		}
		out[i] = st.column(h, records)
	}
	return out
}
