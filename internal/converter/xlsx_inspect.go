package converter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dezhishen/dtool/pkg/types"
	"github.com/xuri/excelize/v2"
)

// InspectRows 是 Inspect 默认采样的数据行数。
//
// 只用来给列类型打置信度，所以不需要全表：几百行已经足够看出「这列是不是混进了
// 文字/日期」，而读全表会让「先看一眼结构」变成一次完整转换。
const InspectRows = 200

// inspectStyleRows 是「读单元格数字格式」的采样行数。
//
// 用 excelize 的样式 API 是逐单元格查询，全表做代价太高；只对前若干行取样，
// 足以判断一列的格式类型（日期/百分比/货币），而这正是规则判不出来的那部分证据。
const inspectStyleRows = 20

// maxQuestions 限制一次 Inspect 抛出的问题数量。
//
// 交互式流程里，一次问二十个问题等于没问：用户会全部按默认走。超出上限的问题
// 降级成 Warnings（仍会显示，但不再阻塞）。
const maxQuestions = 8

// cellClass 是单个单元格在**样本**里的粗分类，只用于置信度与问题探测。
// 与 colStat 的整列判定刻意不同：这里需要「各类各有多少个」，而不是「是否全部满足」。
const (
	classBlank       = "blank"
	classInteger     = "integer"
	classNumber      = "number"
	classBoolean     = "boolean"
	classDate        = "date"
	classLeadingZero = "leading_zero"
	classLongDigits  = "long_digits"
	classZeroOne     = "zero_one" // 只取 0/1：数值上是整数，语义上可能是布尔
	classText        = "text"
)

func classifyCell(raw string) string {
	if isBlank(raw) {
		return classBlank
	}
	v := strings.TrimSpace(raw)
	switch {
	case intRe.MatchString(v) && leadZero.MatchString(v):
		return classLeadingZero
	case intRe.MatchString(v) && len(strings.TrimLeft(v, "-")) > 15:
		return classLongDigits
	case v == "0" || v == "1":
		return classZeroOne
	case intRe.MatchString(v):
		return classInteger
	case floatRe.MatchString(v) && !leadZero.MatchString(v) && len(v) <= 30:
		return classNumber
	}
	if l := strings.ToLower(v); l == "true" || l == "false" {
		return classBoolean
	}
	for _, l := range dateLayouts {
		if _, err := time.Parse(l.in, v); err == nil {
			return classDate
		}
	}
	return classText
}

// Inspect 只读结构：工作表清单、表头所在行、每列的候选类型（带置信度与命中数）
// 以及需要人确认的问题。不写任何产物，可反复调用。
func (c *xlsxConverter) Inspect(o Options) (*Layout, error) {
	ctx := o.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, interruptedErr()
	}
	if _, err := os.Stat(o.Input); err != nil {
		return nil, types.Errorf(types.CodeNotFound, "file not found: %s", o.Input)
	}
	if !strings.EqualFold(filepath.Ext(o.Input), ".xlsx") {
		return nil, types.Errorf(types.CodeUsage, "unsupported file type %q", filepath.Ext(o.Input)).
			WithHint("仅支持 .xlsx")
	}
	f, err := excelize.OpenFile(o.Input)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	sheets := f.GetSheetList()
	sheet := o.Sheet
	if sheet == "" && o.Layout != nil {
		sheet = o.Layout.Sheet
	}
	headerRow := 0
	if o.Layout != nil {
		headerRow = o.Layout.HeaderRow // 带着上一次的确认结果再 inspect：按确认的表头行重算列
	}
	switch {
	case sheet != "":
		if idx, _ := f.GetSheetIndex(sheet); idx < 0 {
			return nil, types.Errorf(types.CodeNotFound, "sheet not found: %s", sheet).
				WithDetail("available sheets: " + strings.Join(sheets, ", "))
		}
	case len(sheets) > 0:
		sheet = sheets[0]
	default:
		return nil, types.Errorf(types.CodeExec, "workbook has no sheets")
	}

	l := &Layout{Format: c.Name(), Version: LayoutVersion, Sheet: sheet, Sheets: sheets}
	if len(sheets) > 1 {
		l.Questions = append(l.Questions, Question{
			Code:    "sheet",
			Prompt:  "工作簿有多个工作表，确认要转换哪一个（当前：" + sheet + "）",
			Options: sheets,
			Default: sheet,
		})
	}
	// 合并单元格只影响「非左上角为空」这一点，属于提示性信息；数量本身很便宜（流式）。
	if n, err := mergeCount(o.Input, sheet); err == nil && n > 0 {
		l.Warnings = append(l.Warnings, fmt.Sprintf("检测到 %d 个合并单元格，仅左上角有值，其余为 null", n))
		l.Questions = append(l.Questions, Question{
			Code:    "merged_cells",
			Prompt:  fmt.Sprintf("有 %d 个合并单元格（非左上角为空），按「只有左上角有值」处理吗？", n),
			Options: []string{"keep", "unmerge_fill"},
			Default: "keep",
		})
	}

	it, err := f.Rows(sheet)
	if err != nil {
		return nil, err
	}
	defer it.Close()

	var (
		headers    []string
		sampleRows []int // 前若干数据行的真实行号，供后面读单元格格式
		counts     []map[string]int
		gotHeader  int // 实际认定的表头行号
		headerWide int // 表头行的非空单元格数
		dataWide   int // 样本数据行的最大非空单元格数
		sampled    int
		totalRows  []int
		rownum     int
	)
	for it.Next() {
		rownum++
		if rownum%rowsCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return nil, interruptedErr()
			}
		}
		cells, err := it.Columns()
		if err != nil {
			return nil, err
		}
		if allBlank(cells) {
			continue
		}
		if headers == nil {
			if headerRow > 0 && rownum != headerRow {
				continue // 跳过确认过的表头行之前的内容（标题/说明行）
			}
			headers, l.Warnings = normalizeHeaders(cells, l.Warnings)
			gotHeader = rownum
			headerWide = nonBlankCount(cells)
			counts = make([]map[string]int, len(headers))
			for i := range counts {
				counts[i] = map[string]int{}
			}
			continue
		}
		if n := nonBlankCount(cells); n > dataWide {
			dataWide = n
		}
		if sampled < InspectRows && looksLikeTotalRow(cells) {
			totalRows = append(totalRows, rownum)
		}
		for i := range headers {
			v := ""
			if i < len(cells) {
				v = cells[i]
			}
			counts[i][classifyCell(v)]++
		}
		if sampled < inspectStyleRows {
			sampleRows = append(sampleRows, rownum)
		}
		sampled++
		if sampled >= InspectRows {
			break
		}
	}
	if err := it.Error(); err != nil {
		return nil, err
	}
	if headers == nil {
		return nil, types.Errorf(types.CodeExec, "sheet %q is empty", sheet)
	}

	l.HeaderRow = gotHeader
	if headerRow == 0 && headerWide <= 1 && dataWide > 1 {
		l.Questions = append(l.Questions, Question{
			Code:    "header_row",
			Row:     gotHeader,
			Prompt:  "第 " + strconv.Itoa(gotHeader) + " 行只有 1 个非空单元格，可能是标题行而不是表头",
			Options: []string{"keep", "next"},
			Default: "keep",
		})
	}
	if len(totalRows) > 0 {
		l.Questions = append(l.Questions, Question{
			Code:    "total_row",
			Row:     totalRows[0],
			Prompt:  "样本里第 " + strconv.Itoa(totalRows[0]) + " 行看起来是汇总行（合计/总计/小计/total）",
			Options: []string{"keep", "drop"},
			Default: "drop",
		})
	}

	l.Columns = make([]LayoutColumn, len(headers))
	for i, h := range headers {
		l.Columns[i] = layoutColumn(h, counts[i], sampled)
		if q, ok := ambiguityQuestion(l.Columns[i]); ok {
			l.Questions = append(l.Questions, q)
		}
	}
	// 用单元格数字格式补一条强证据：Excel 自己说这列是日期/百分比/货币时，
	// 仅凭取值形态可能会漏判（如日期被渲染成 "12-09-25"、百分比是 "12%"）。
	c.applyExcelFormats(f, sheet, l, sampleRows)

	if len(l.Questions) > maxQuestions {
		for _, q := range l.Questions[maxQuestions:] {
			l.Warnings = append(l.Warnings, "未询问："+q.Prompt)
		}
		l.Questions = l.Questions[:maxQuestions]
	}
	return l, nil
}

// layoutColumn 由样本命中数推出列结构：主类型 + 置信度 + 证据。
func layoutColumn(name string, counts map[string]int, sampled int) LayoutColumn {
	col := LayoutColumn{Name: name, Source: SourceDetected, Evidence: counts}
	nonNull := sampled - counts[classBlank]
	if nonNull <= 0 {
		col.Type = "null"
		col.Note = "样本里没有非空值"
		return col
	}
	// 前导零/超长数字是**一票否决**：只要出现一个，整列就必须按文本处理
	// （与 colStat 的整列判定同一规则）。否则 Inspect 说 integer、Convert 写成
	// string，用户按 Inspect 的结果改 Layout 只会越改越乱。
	// 0/1 列：规则分不开「是不是布尔」，把两个候选都留给调用方判断。
	if counts[classZeroOne] == nonNull {
		col.Type = "integer"
		col.Confidence = 1
		col.Candidates = []string{"integer", "boolean"}
		col.Note = "只取 0/1：按数值是 integer，按语义可能是 boolean"
		return col
	}
	if counts[classLeadingZero] > 0 || counts[classLongDigits] > 0 {
		col.Type = "string"
		col.Confidence = 1 // 作为文本，所有取值都成立
		switch {
		case counts[classLeadingZero] > 0:
			col.Note = "含前导零的数字（如订单号 007123），按文本处理以免丢位"
		default:
			col.Note = "含超过 15 位的数字，按文本处理以免精度丢失"
		}
		return col
	}
	dominant, best := classText, -1
	for _, cls := range []string{classInteger, classZeroOne, classNumber, classBoolean, classDate, classText} {
		if counts[cls] > best {
			dominant, best = cls, counts[cls]
		}
	}
	col.Confidence = float64(best) / float64(nonNull)
	if best < nonNull {
		// 样本里不是同一种类型：按「过滤后剩多个」处理，默认取最安全的 string
		// （保留原值，绝不因多数派而丢掉少数派的写法），其余类型作为候选交给调用方。
		col.Type = "string"
		col.Candidates = []string{typeOfClass(dominant), "string"}
		col.Note = "样本里混有多种取值，默认按 string 保留原值"
		return col
	}
	switch dominant {
	case classInteger:
		col.Type = "integer"
	case classNumber:
		col.Type = "number"
	case classBoolean:
		col.Type = "boolean"
	case classDate:
		col.Type = "date"
		// Format 留空：样本只能判断「像日期」，具体布局（ISO / 斜杠 / 带时间）
		// 由 Convert 阶段的整列判定给出，避免两处各说一套。
	default:
		col.Type = "string"
	}
	return col
}

// ambiguityQuestion 针对「规则过滤后仍剩多个候选」的列提问：这些候选是按规则无法
// 区分、只能靠业务含义决定的（目前只有 0/1）。只有一个候选的列不会出现在这里。
func ambiguityQuestion(col LayoutColumn) (Question, bool) {
	if len(col.Candidates) < 2 {
		return Question{}, false
	}
	return Question{
		Code:    "type_ambiguity",
		Column:  col.Name,
		Prompt:  "列 " + col.Name + " 的取值同时符合 " + strings.Join(col.Candidates, " 与 ") + "，确认按哪个处理？",
		Options: col.Candidates,
		Default: col.Type,
	}, true
}

func nonBlankCount(cells []string) int {
	n := 0
	for _, c := range cells {
		if !isBlank(c) {
			n++
		}
	}
	return n
}

// totalRowMarkers 是汇总行的常见写法：这些行混进数据会毁掉数值分布与整体类型推断。
var totalRowMarkers = []string{"合计", "总计", "小计", "汇总", "total", "subtotal"}

func looksLikeTotalRow(cells []string) bool {
	for _, c := range cells {
		v := strings.ToLower(strings.TrimSpace(c))
		if v == "" {
			continue
		}
		for _, m := range totalRowMarkers {
			if strings.Contains(v, m) {
				return true
			}
		}
		return false // 只看第一个非空单元格，避免"备注里有'合计'字样"造成误报
	}
	return false
}

// applyLayout 用确认过的结构覆盖探测结果：列名、列类型（必要时还有日期布局）。
//
// 只覆盖「判断」出来的东西，不覆盖「统计」出来的东西：nullable / unique /
// distinct 仍来自整列扫描——它们是事实，不是猜测，人工不该去改。
func applyLayout(headers []string, schema []ColumnSchema, stats []*colStat, l *Layout) error {
	seen := make(map[string]int, len(headers))
	for i := range schema {
		if i >= len(l.Columns) {
			break
		}
		lc := l.Columns[i]
		if name := strings.TrimSpace(lc.Name); name != "" {
			headers[i] = name
			schema[i].Name = name
		}
		want := strings.ToLower(strings.TrimSpace(lc.Type))
		if want == "text" {
			want = "string" // 容错：Layout 里写 text 等同于 string
		}
		if want == "" {
			continue
		}
		// 强制类型只允许「整列确实满足」的那种：把混了文本的列硬声明成 integer，
		// convertCell 会把这些值解析失败，结果变成 0——静默的数据损坏比报错严重得多。
		// string 例外：任何取值都能按文本保存，这正是「我不确定时保留原样」的表达。
		got, gotFormat := stats[i].typ()
		// 唯一的例外是 0/1 列选 boolean：规则上无损（1/0 ↔ true/false），
		// 而且这正是「剩下多个候选时由人定夺」的那一种情况。
		if want != got && want != "string" && !(want == "boolean" && got == "integer" && stats[i].zeroOneOK) {
			return types.Errorf(types.CodeUsage,
				"列 %s 在表结构里声明为 %s，但整列扫描得到 %s，不能强制转换", schema[i].Name, want, got).
				WithHint("把该列改成 string（保留原值），或先清洗这列里的异常取值再确认结构")
		}
		schema[i].Type = want
		schema[i].SQLType = sqlType(want)
		schema[i].Source = SourceConfirmed
		switch {
		case lc.Format != "":
			schema[i].Format = lc.Format
		case want == "date":
			schema[i].Format = gotFormat
		}
	}
	for i, h := range headers {
		if j, dup := seen[h]; dup {
			return types.Errorf(types.CodeUsage, "表结构里第 %d 列与第 %d 列同名（%s）", j+1, i+1, h).
				WithHint("修改 Layout 里的列名，保证互不重复")
		}
		seen[h] = i
	}
	return nil
}

// applyExcelFormats 用单元格的数字格式给列补证据，并在「格式与取值冲突」时提问。
//
// 冲突的典型场景：单元格是日期格式，但取值形态既像整数（Excel 序列号）又像别的
// 短横线数字（mm-dd-yy 在 dd-mm-yy 地区会被误读）。这时只靠字符串规则分不开，
// 而 Excel 的格式声明是唯一权威信息——所以把它变成候选/问题，而不是悄悄猜。
func (c *xlsxConverter) applyExcelFormats(f *excelize.File, sheet string, l *Layout, rows []int) {
	if len(rows) == 0 || len(l.Columns) == 0 {
		return
	}
	kinds := map[int]string{} // 样式 ID -> 格式类别；同一样式只解析一次
	counts := make([]map[string]int, len(l.Columns))
	for _, rownum := range rows {
		for i := range l.Columns {
			cell, err := excelize.CoordinatesToCellName(i+1, rownum)
			if err != nil {
				continue
			}
			id, err := f.GetCellStyle(sheet, cell)
			if err != nil {
				continue
			}
			kind, ok := kinds[id]
			if !ok {
				kind = excelFormatKind(f, id)
				kinds[id] = kind
			}
			if kind == "" {
				continue
			}
			if counts[i] == nil {
				counts[i] = map[string]int{}
			}
			counts[i][kind]++
		}
	}
	for i := range l.Columns {
		for kind, n := range counts[i] {
			if l.Columns[i].Evidence == nil {
				l.Columns[i].Evidence = map[string]int{}
			}
			l.Columns[i].Evidence["excel_"+kind] = n
		}
		// Excel 说是日期、取值却不是日期：把 date 加进候选并要求确认。
		col := &l.Columns[i]
		if counts[i]["date"] > 0 && col.Type != "date" && col.Type != "null" {
			col.Candidates = []string{col.Type, "date"}
			l.Questions = append(l.Questions, Question{
				Code:    "date_format",
				Column:  col.Name,
				Prompt:  "列 " + col.Name + " 的单元格是 Excel 日期格式，但取值按 " + col.Type + " 解释；确认按日期处理吗？",
				Options: []string{"date", col.Type},
				Default: "date",
			})
		}
	}
}

// excelFormatKind 把单元格数字格式归成三类有价值的信息；没有信号时返回空串。
// 内置数字格式编号见 ECMA-376（14~22、45~47 日期时间，9/10 百分比，5~8、42~44 货币）。
func excelFormatKind(f *excelize.File, styleID int) string {
	if styleID == 0 {
		return ""
	}
	st, err := f.GetStyle(styleID)
	if err != nil {
		return ""
	}
	switch nf := st.NumFmt; {
	case (nf >= 14 && nf <= 22) || (nf >= 45 && nf <= 47):
		return "date"
	case nf == 9 || nf == 10:
		return "percent"
	case (nf >= 5 && nf <= 8) || (nf >= 42 && nf <= 44):
		return "currency"
	}
	if st.CustomNumFmt != nil {
		// 引号里是字面文本（如 "年"、"月"），不能当作日期标记。
		lower := strings.ToLower(*st.CustomNumFmt)
		bare := strings.SplitN(lower, "\"", 2)[0]
		switch {
		case strings.ContainsAny(bare, "ymd"):
			return "date"
		case strings.Contains(bare, "%"):
			return "percent"
		case strings.ContainsAny(bare, "$¥￥"):
			return "currency"
		}
	}
	return ""
}

// typeOfClass 把样本分类映射回类型名，用于候选列表。
func typeOfClass(class string) string {
	switch class {
	case classInteger, classZeroOne:
		return "integer"
	case classNumber:
		return "number"
	case classBoolean:
		return "boolean"
	case classDate:
		return "date"
	}
	return "string"
}
