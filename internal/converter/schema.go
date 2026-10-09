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

// colStat 逐行累积单列的统计信息；InferSchema 与流式转换共用它，
// 保证「整列推断」的规则只有一份实现。
type colStat struct {
	nonNull int
	seen    map[string]struct{}
	order   []string // 前 enumLimit+1 个不同值，按首次出现顺序
	over    bool     // 不同值超过 uniqueLimit，此时不再跟踪 seen/order

	intOK  bool // 迄今所有非空值都满足整数规则（含 ≤15 位、无前导零）
	numOK  bool // ... 都满足数字规则
	boolOK bool // ... 都是 true/false
	dateOK []bool

	hasNum bool
	min    *float64
	max    *float64
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
				c.over = true // 与原实现一致：溢出后不再收集，也不再判定 unique/enum
			} else {
				c.seen[v] = struct{}{}
				if len(c.order) < enumLimit+1 {
					c.order = append(c.order, v)
				}
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
	// 最小/最大值：整数规则蕴含数字规则，因此只在 numOK 时解析即可
	// （原实现在最终类型为 integer/number 时也会解析全部非空值）。
	if c.numOK {
		f, _ := strconv.ParseFloat(v, 64)
		if c.min == nil || f < *c.min {
			m := f
			c.min = &m
		}
		if c.max == nil || f > *c.max {
			m := f
			c.max = &m
		}
		c.hasNum = true
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

// column 依据累积结果产出列 Schema。
func (c *colStat) column(name string, records int) ColumnSchema {
	typ, format := c.typ()
	cs := ColumnSchema{Name: name, Type: typ, SQLType: sqlType(typ), Format: format,
		Nullable: c.nonNull < records}
	if !c.over {
		cs.Unique = c.nonNull == records && len(c.seen) == records && records > 0
		if typ == "string" && len(c.seen) <= enumLimit && len(c.seen) < c.nonNull {
			cs.Enum = c.order
		}
	}
	for _, v := range c.order {
		if len(cs.Samples) >= sampleCount {
			break
		}
		cs.Samples = append(cs.Samples, convertCell(typ, v))
	}
	if typ == "integer" || typ == "number" {
		cs.Min, cs.Max = c.min, c.max
	}
	return cs
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
