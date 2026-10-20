package converter

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dezhishen/dtool/pkg/types"
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
	// Candidates 是「按规则过滤之后仍然成立」的多个类型（仅当无法自动定夺时出现）。
	// 只有一个候选时不需要它——工具直接定；出现多个说明规则无法区分，需要人/agent
	// 按业务含义选一个（如 0/1 既可以是 integer，也可以是 boolean）。
	Candidates []string `json:"candidates,omitempty"`
	// Source 标记这个类型是探测出来的还是人工确认过的（见 Layout.Columns）。
	// 它让下游能区分「工具猜的」和「人认过的」，也是重新跑转换时不必再猜的依据。
	Source string `json:"source,omitempty"`
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
	// excelize 会把「日期格式」的单元格按数字格式渲染成字符串，常见的两种内置格式
	// （NumFmt 14 的 mm-dd-yy、NumFmt 22 的 m/d/yy h:mm）必须认得，
	// 否则整列会被判成 string——这类列在中文报表里非常多。
	{"01-02-06", "2006-01-02"},           // mm-dd-yy
	{"1-2-06", "2006-01-02"},             // m-d-yy
	{"1/2/06", "2006-01-02"},             // m/d/yy
	{"1/2/06 15:04", "2006-01-02T15:04"}, // m/d/yy h:mm
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
	// zeroOneOK 记录「所有非空整数都只取 0/1」——这是唯一一种规则无法定夺的
	// 情况：按数值是 integer，按语义可能是 boolean。是否真是布尔，交给用户/agent 定。
	zeroOneOK bool
	dateOK    []bool
}

func newColStat() *colStat {
	c := &colStat{seen: map[string]struct{}{}, intOK: true, numOK: true, boolOK: true, zeroOneOK: true}
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
	if c.zeroOneOK && v != "0" && v != "1" {
		c.zeroOneOK = false
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

// candidates 返回「按规则过滤后仍然成立」的类型集合，按最具体优先缩减：
// string 被任何其他类型覆盖，number 被 integer 覆盖，因此它们不会并列出现。
//
// 过滤器只做**排除**，不做加权投票：任一非空值不满足就淘汰该类型。这样剩下的
// 候选要么只有一个（直接定），要么是规则本来就分不开的语义歧义（0/1 → integer 或 boolean）。
func (c *colStat) candidates(typ string) []string {
	switch typ {
	case "integer":
		if c.zeroOneOK {
			return []string{"integer", "boolean"}
		}
		return []string{"integer"}
	case "number", "boolean", "date":
		return []string{typ}
	}
	return nil // 轮到 string 时没有可选项：它是兜底，不是选择
}

// column 依据累积结果产出列 Schema（只含结构，不含原始取值）。
func (c *colStat) column(name string, records int) ColumnSchema {
	typ, format := c.typ()
	cs := ColumnSchema{Name: name, Type: typ, SQLType: sqlType(typ), Format: format,
		Nullable: c.nonNull < records}
	if cands := c.candidates(typ); len(cands) > 1 {
		cs.Candidates = cands
	}
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
		switch strings.ToLower(t) {
		case "true", "1":
			return true
		case "false", "0":
			return false
		}
		return nil
	case "date":
		for _, l := range dateLayouts {
			if tm, err := time.Parse(l.in, t); err == nil {
				return tm.Format(l.out)
			}
		}
	}
	return s
}

// InferSchemaFromRows 从结果集的类型化行推断 Schema，并规整为统一列顺序。
// 结果集可能来自 query（JSON 行），这里复用同一套整列类型推断规则。
func InferSchemaFromRows(rows []types.Row) ([]string, []ColumnSchema, []types.Row) {
	seen := map[string]bool{}
	var headers []string
	for _, r := range rows {
		for _, c := range r.Columns {
			if !seen[c] {
				seen[c] = true
				headers = append(headers, c)
			}
		}
	}
	data := make([][]string, len(headers))
	norm := make([]types.Row, len(rows))
	for i, r := range rows {
		vals := make([]any, len(headers))
		for j, h := range headers {
			if v, ok := r.Get(h); ok {
				vals[j] = v
				data[j] = append(data[j], valueString(v))
			} else {
				data[j] = append(data[j], "")
			}
		}
		norm[i] = types.Row{Columns: headers, Values: vals}
	}
	return headers, InferSchema(headers, data, len(rows)), norm
}

func valueString(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	default:
		return fmt.Sprint(x)
	}
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
