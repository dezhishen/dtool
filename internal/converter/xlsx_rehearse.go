package converter

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/xuri/excelize/v2"
)

const (
	// rehearseHeaderRows 只探前几个非空行作为表头候选：真实报表的标题区最多几行，
	// 再往后就属于数据区了，把它当表头既无意义也拖慢预演练。
	rehearseHeaderRows = 4
	// rehearseRows 是每个候选方案采样的数据行数：只用来比较方案优劣，不做全表统计。
	rehearseRows = 50
)

// sheetProbe 是一个候选方案在样本上的表现。
type sheetProbe struct {
	sheet     string
	headerRow int      // 1 基；0 表示「第一个非空行」
	columns   []string // 这一假设下认出的表头
	dataRows  int      // 样本内数据行数
	mismatch  int      // 列数与表头不一致的数据行数
	maxWidth  int      // 样本内最宽的一行有多少个非空单元格
}

// Rehearse 生成预演练：当前默认方案 + 每个决策点的候选方案及其影响。
//
// 只读、不落产物，可以反复跑。它比 Inspect 多做三件事：
//  1. 把「表头可能是第 1/2/3 行」这种假设各试一遍，报告各自的列数与对齐情况；
//  2. 多工作表时把每张表都探一遍，而不是只报第一张；
//  3. 把每列的类型候选（含占比）摊开，让「这列到底算数字还是文本」变得可选。
func (c *xlsxConverter) Rehearse(o Options) (*Rehearsal, error) {
	l, err := c.Inspect(o) // 默认方案与 Inspect 完全同源，避免两处结论不一致
	if err != nil {
		return nil, err
	}
	ctx := o.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	f, err := excelize.OpenFile(o.Input)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	r := &Rehearsal{Layout: l}

	// 1) 工作表候选：多表时逐张探测，让「选错表」变成可比较的选项。
	if len(l.Sheets) > 1 {
		for _, sh := range l.Sheets {
			p, err := probeSheet(ctx, f, sh, 0, rehearseRows)
			if err != nil {
				return nil, err
			}
			if p == nil {
				continue // 空表：不给候选，免得诱导用户选它
			}
			r.Candidates = append(r.Candidates, Candidate{
				Code:   "sheet",
				Value:  "sheet=" + sh,
				Label:  "用工作表 " + sh,
				Score:  probeScore(p),
				Effect: p.describe(),
			})
		}
	}

	// 2) 表头行候选：只在前几个非空行里挑，跳过空行避免占用名额。
	var probes []*sheetProbe
	for row, seen := 1, 0; row <= rehearseHeaderRows && seen < rehearseHeaderRows; row++ {
		p, err := probeSheet(ctx, f, l.Sheet, row, rehearseRows)
		if err != nil {
			return nil, err
		}
		if p == nil || len(p.columns) == 0 {
			continue
		}
		seen++
		probes = append(probes, p)
		// 默认不把候选行的单元格原文写进标签：表头行猜错时，那些"列名"其实是数据值。
		label := fmt.Sprintf("第 %d 行当表头（%d 列）", p.headerRow, len(p.columns))
		if o.IncludeSamples {
			label = fmt.Sprintf("第 %d 行当表头（%d 列：%s）", p.headerRow, len(p.columns), shortNames(p.columns))
		}
		r.Candidates = append(r.Candidates, Candidate{
			Code:   "header_row",
			Value:  fmt.Sprintf("header_row=%d", p.headerRow),
			Label:  label,
			Score:  probeScore(p),
			Effect: p.describe(),
		})
	}

	// 3) 列类型候选：只给「样本里混了类型」的列，纯类型列没有可选项。
	//
	// 用**打分最高的表头假设**算列，而不是默认方案的列：默认可能正好选错了表头行，
	// 那样报出来的列名是错的，按它给类型候选只会误导人。
	base := l
	if best := bestProbe(probes); best != nil && best.headerRow != l.HeaderRow {
		if l2, err := c.Inspect(Options{Ctx: ctx, Sheet: l.Sheet, Input: o.Input,
			Layout: &Layout{Format: l.Format, Version: l.Version, Sheet: l.Sheet, HeaderRow: best.headerRow}}); err == nil {
			base = l2
		}
	}
	for _, col := range base.Columns {
		r.Candidates = append(r.Candidates, typeCandidates(col)...)
		// 规则分不开的语义歧义（0/1、Excel 日期格式冲突）也要摊开：
		// 这时没有「更对」的答案，只有用户/agent 按业务含义做的选择。
		if len(col.Candidates) < 2 {
			continue
		}
		for _, t := range col.Candidates {
			r.Candidates = append(r.Candidates, Candidate{
				Code:   "type_ambiguity",
				Column: col.Name,
				Value:  "type=" + t,
				Label: fmt.Sprintf("把列 %s 当 %s 处理（规则无法区分 %s）",
					col.Name, t, strings.Join(col.Candidates, " / ")),
				Score:  0.5, // 规则分不开时不给假精度：并列
				Effect: "两种解释都符合规则；选择只影响该列在数据集里的形态",
			})
		}
	}

	sort.SliceStable(r.Candidates, func(i, j int) bool {
		a, b := r.Candidates[i], r.Candidates[j]
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		return a.Score > b.Score
	})
	return r, nil
}

// probeSheet 按「第 headerRow 行是表头」的假设扫样本；headerRow 为 0 时按第一个非空行。
// 工作表为空（没有表头）时返回 nil。
func probeSheet(ctx context.Context, f *excelize.File, sheet string, headerRow, sample int) (*sheetProbe, error) {
	it, err := f.Rows(sheet)
	if err != nil {
		return nil, err
	}
	defer it.Close()
	p := &sheetProbe{sheet: sheet, headerRow: headerRow}
	var rownum int
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
		if p.columns == nil {
			if headerRow > 0 && rownum != headerRow {
				continue
			}
			names, _ := normalizeHeaders(cells, nil)
			p.columns = names
			p.headerRow = rownum
			if w := nonBlankCount(cells); w > p.maxWidth {
				p.maxWidth = w
			}
			continue
		}
		p.dataRows++
		if w := nonBlankCount(cells); w > p.maxWidth {
			p.maxWidth = w
		}
		if w := nonBlankCount(cells); w != len(p.columns) {
			p.mismatch++
		}
		if p.dataRows >= sample {
			break
		}
	}
	if err := it.Error(); err != nil {
		return nil, err
	}
	if p.columns == nil {
		return nil, nil
	}
	return p, nil
}

// probeScore 是候选的启发式打分（0~1）。它不是概率，只用于在组内排序：
//
//   - 表头宽度：表头的列数应当覆盖数据区最宽的行（标题行往往只有 1 列，会被压低）；
//   - 对齐度：数据行的列数与表头一致的比例（多级表头/备注列会导致不齐）；
//   - 有数据：假设成立时必须至少能读到数据行，否则多半选错了行。
func probeScore(p *sheetProbe) float64 {
	if p == nil || len(p.columns) == 0 {
		return 0
	}
	width := 1.0
	if p.maxWidth > 0 {
		width = math.Min(1, float64(len(p.columns))/float64(p.maxWidth))
	}
	consistency := 1.0
	if p.dataRows > 0 {
		consistency = 1 - float64(p.mismatch)/float64(p.dataRows)
	}
	score := 0.5*width + 0.5*consistency
	if p.dataRows == 0 {
		score *= 0.5 // 只有表头没有数据：可能是说明行
	}
	return math.Round(score*100) / 100
}

// describe 用一句话说明「选了这个方案会得到什么」，避免用户靠猜。
func (p *sheetProbe) describe() string {
	return fmt.Sprintf("列数 %d；样本内数据行 %d；列宽不齐 %d 行", len(p.columns), p.dataRows, p.mismatch)
}

// shortNames 取前几个列名拼成提示，列多时省略。
func shortNames(names []string) string {
	const max = 4
	if len(names) <= max {
		return strings.Join(names, "/")
	}
	return strings.Join(names[:max], "/") + fmt.Sprintf("/…共 %d 列", len(names))
}

// typeCandidates 把一列的样本命中数展开成类型候选（同类型合并、按占比降序）。
// 只有一种类型时返回 nil——没有可选项就不该出现在待选清单里。
func typeCandidates(col LayoutColumn) []Candidate {
	if len(col.Evidence) == 0 {
		return nil
	}
	nonNull := 0
	for cls, n := range col.Evidence {
		if cls != classBlank {
			nonNull += n
		}
	}
	if nonNull == 0 {
		return nil
	}
	agg := map[string]int{}
	for _, m := range []struct{ class, typ string }{
		{classInteger, "integer"},
		{classNumber, "number"},
		{classBoolean, "boolean"},
		{classDate, "date"},
		{classText, "string"},
		{classLeadingZero, "string"},
		{classLongDigits, "string"},
	} {
		agg[m.typ] += col.Evidence[m.class]
	}
	type pick struct {
		typ string
		n   int
	}
	picks := make([]pick, 0, len(agg))
	for typ, n := range agg {
		if n > 0 {
			picks = append(picks, pick{typ, n})
		}
	}
	if len(picks) < 2 {
		return nil
	}
	sort.Slice(picks, func(i, j int) bool {
		if picks[i].n != picks[j].n {
			return picks[i].n > picks[j].n
		}
		return picks[i].typ < picks[j].typ
	})
	if len(picks) > 3 {
		picks = picks[:3]
	}
	out := make([]Candidate, 0, len(picks))
	for _, pk := range picks {
		out = append(out, Candidate{
			Code:   "type_ambiguity",
			Column: col.Name,
			Value:  fmt.Sprintf("type=%s", pk.typ),
			Label:  fmt.Sprintf("把列 %s 当 %s 处理（样本 %d/%d 个值符合）", col.Name, pk.typ, pk.n, nonNull),
			Score:  math.Round(float64(pk.n)/float64(nonNull)*100) / 100,
			Effect: fmt.Sprintf("该列现有取值中 %d 个能原样转成 %s，其余将按字符串保留", pk.n, pk.typ),
		})
	}
	return out
}

// bestProbe 返回打分最高的表头假设；并列时取行号靠前的（保守：不无故下移表头）。
func bestProbe(probes []*sheetProbe) *sheetProbe {
	var best *sheetProbe
	bestScore := -1.0
	for _, p := range probes {
		score := probeScore(p)
		if score > bestScore {
			best, bestScore = p, score
		}
	}
	return best
}
