package converter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dezhishen/dtool/pkg/types"
)

// Options 是一次转换的全部输入。
//
// 它同时承载「选哪个转换器」（Converter / ConverterOpts）和「转换什么」
// （Input / Sheet / OutDir…）。前者是装配期的事，后者是执行期的事；分开的原因是
// 调用方（CLI、pipeline、Action 预览）只想填一次结构体，不想记两套参数。
type Options struct {
	// Ctx 用于响应中断（Ctrl+C / SIGTERM）；nil 表示不检查。
	Ctx context.Context
	// Converter 选择转换器：空串与 auto 都表示「按输入扩展名自动选」，
	// 也可以写已注册的名字（见 Names）。
	Converter string
	// ConverterOpts 是传给 Converter.Configure 的配置，即配置文件
	// convert.converter.<name> 下的全部键值（nil 表示没有配置）。
	ConverterOpts map[string]any
	// IncludeSamples 决定**探测输出**里能否带单元格原文（仅 Rehearse 用）。
	//
	// 默认 false = 强隐私行为：预演练只报「第 2 行当表头、7 列、样本 4 行」这类结构信息。
	// 表头行猜错时，候选行的前几个单元格其实是**数据值**，只有用户把配置改成
	// privacy: normal 后才会显示——那一步由 CLI 决定，转换器自己不做隐私判断。
	IncludeSamples bool
	// Layout 是已确认的表结构（通常来自上一次 Inspect 的结果）。非 nil 时转换器
	// 必须按它来读——表头行、列名、列类型都不再自己猜；与文件实际情况对不上就报错，
	// 而不是回退到自动探测（那会让「我明明确认过」变成「怎么又不一样了」）。
	Layout      *Layout
	Input       string
	Sheet       string
	OutDir      string
	UpdatedAt   time.Time // 写入 Schema 的更新日期，零值取当前时间
	PreviewRows int       // 保留多少条类型化记录供调用方预览（0 表示不留）
}

// Result 是转换产物。字段与具体输入格式无关：任何转换器都要产出
// data.json + data.schema.json，并回填行数、列名与预览。
type Result struct {
	DataFile    string
	SchemaFile  string
	Sheet       string
	Columns     []string
	RecordCount int
	Preview     []types.Row // 前 PreviewRows 条记录（类型化），供 Action 预览
	Warnings    []string
}

// Converter 是「表格文件 → 数据集」的差异面。
//
// 与 query.Engine 同一个思路：dtool 的下游（dataset / query / pipeline / agent）
// 只认 data.json + data.schema.json 这一种形状，所以格式之间的真正差异只有
// 几件事——认哪个扩展名、怎么把单元格读出来、有没有自己的可调参数、日志里怎么称呼自己。
//
// 每个转换器在自己的文件里用 init 注册自己（见 registry.go），不在这里列清单：
// 加一种格式只新增一个文件，不动这个接口。
type Converter interface {
	// Name 返回转换器标识（xlsx），用于日志、错误与结果记录。
	Name() string
	// Extensions 返回它能处理的文件扩展名（小写、含点），auto 选择与
	// 显式指定时的匹配校验都用它。
	Extensions() []string
	// Configure 是初始化回调：每次装配转换器时调用一次，opts 是配置文件
	// convert.converter.<name> 下的全部键值（nil 表示没有配置）。
	//
	// 约定：未知键必须报错——配置拼错却被静默忽略，是最难查的一类问题；
	// 同一实例上重复调用应当幂等。
	Configure(opts map[string]any) error
	// Inspect 只读表结构，返回一份待确认的 Layout：不写 data.json、不落数据集，
	// 因此可以反复调用（交互式流程就是这样：Inspect → 人/agent 调整 → Convert 带 Layout）。
	//
	// 它必须便宜：只读元数据与有限样本，不做全表统计。代价高的判定留给 Convert。
	Inspect(o Options) (*Layout, error)
	// Rehearse 是 Inspect 的「多方案预演练」：把每个待确认的点展开成若干候选
	// （表头行可能是第几行、用哪张表、这一列还能是什么类型），并给出各自的
	// 打分与影响（列数、样本行数、列名变化）。它同样只读、不落产物。
	Rehearse(o Options) (*Rehearsal, error)
	// Convert 执行转换，结果写到 Options.OutDir 下。
	Convert(o Options) (*Result, error)
}

// Rehearsal 是一次预演练：当前默认方案 + 每个决策点的候选方案。
//
// 为什么要有它：结构提取是反复交互的过程，而「只给一个答案」的选错代价很高——
// 用户只会看到「列名不对」，却不知道该改哪个参数。把候选摊开（各自带打分与影响），
// 用户/agent 就能一眼看出「原来这份表头在第 3 行」「原来这列更可能是文本」，
// 直接把选中的候选合并回 Layout 再跑，不必试探性地改参数重跑。
type Rehearsal struct {
	// Layout 是当前默认方案（等价于 Inspect 的结果）。
	Layout *Layout `json:"layout"`
	// Candidates 是各决策点的候选，按 Code 分组、组内按 Score 降序。
	Candidates []Candidate `json:"candidates"`
}

// Candidate 是一个候选方案：选它会发生什么，以及有多大把握。
type Candidate struct {
	// Code 与 Question.Code 对应（sheet / header_row / mixed_type / total_row…）。
	Code string `json:"code"`
	// Column 仅在列级决策（mixed_type）时出现。
	Column string `json:"column,omitempty"`
	// Value 是机器可读的取值（如 header_row=2、"type=string"），用于合并回 Layout。
	Value string `json:"value"`
	// Label 是给人看的一句话。
	Label string `json:"label"`
	// Score 是启发式打分（0~1），只用于组内排序与提示，不代表概率。
	Score float64 `json:"score"`
	// Effect 选它之后的影响：列数 / 样本内数据行 / 列名变化等。
	Effect string `json:"effect,omitempty"`
}

// Layout 是「表格结构」的可确认稿：探测结果 + 人工/agent 的修改 + 待确认的问题。
//
// 为什么要独立成一个结构，而不是让转换器内部悄悄推断：
//
//	结构提取天然是**反复交互**的过程（多级表头、合并单元格、汇总行、列类型混杂——
//	真实的 Excel 报表几乎都会撞上其中一条）。如果推断结果只活在 Convert 内部，
//	调用方唯一的纠错手段就是「改代码/改文件再重跑」；把它显式化之后，流程变成
//	Inspect → 看 Questions → 改 Layout（可存盘、可 diff、可回传）→ Convert，
//	同一份 Layout 反复使用还会得到同一份结果，便于比对「改了哪一处、产物变了什么」。
type Layout struct {
	// Format 是产出它的转换器名；换转换器后旧 Layout 不再适用。
	Format string `json:"format"`
	// Version 是结构版本：字段语义变化时递增，让旧 Layout 被明确拒绝而不是误读。
	Version int `json:"version"`
	// Sheet 是选中的工作表；Sheets 是可选清单（多于一个时应当向用户确认）。
	Sheet  string   `json:"sheet,omitempty"`
	Sheets []string `json:"sheets,omitempty"`
	// HeaderRow 是表头所在行（1 基）。0 表示「按第一个非空行」（自动探测的默认口径）。
	HeaderRow int `json:"header_row,omitempty"`
	// Columns 是列结构（名字与类型）。每列带 Source：detected（探测）或
	// confirmed（人确认过），重新探测不得覆盖 confirmed。
	Columns []LayoutColumn `json:"columns,omitempty"`
	// Questions 是**必须由人来定**的点（阻塞级）：每个都带可选项与默认值，
	// 交互时按 order 逐条问；全部回答后得到的 Layout 才是「已确认」的。
	Questions []Question `json:"questions,omitempty"`
	// Warnings 是已自动处理、但用户应当知道的点（比如空表头已重命名）。
	Warnings []string `json:"warnings,omitempty"`
}

// LayoutVersion 是当前 Layout 的结构版本。
const LayoutVersion = 1

// 列的来源标记：探测得来还是人确认过。
const (
	SourceDetected  = "detected"
	SourceConfirmed = "confirmed"
)

// LayoutColumn 是一列的结构。
type LayoutColumn struct {
	Name   string `json:"name"`
	Type   string `json:"type"`             // integer / number / boolean / date / string
	Format string `json:"format,omitempty"` // 日期布局等附加信息
	Source string `json:"source,omitempty"`
	// Candidates 是规则过滤后仍成立的多个类型（只有规则分不开时才出现，如 0/1）。
	// Type 是其中的默认项；调用方在 Candidates 里选另一个并回填 Type 即可。
	Candidates []string `json:"candidates,omitempty"`
	// Confidence 是该类型的判定置信度（0~1）：样本里符合该类型的比例。
	// 它存在的意义是让「我不确定」可见，而不是让工具硬给一个答案。
	Confidence float64 `json:"confidence,omitempty"`
	// Evidence 是判定依据（各类取值命中数），不含具体取值本身，隐私模式下可照常输出。
	Evidence map[string]int `json:"evidence,omitempty"`
	Note     string         `json:"note,omitempty"`
}

// Question 是一个待确认的结构问题。Code 稳定、可编程判断；Prompt 给人看。
type Question struct {
	Code    string   `json:"code"`
	Column  string   `json:"column,omitempty"`
	Row     int      `json:"row,omitempty"`
	Prompt  string   `json:"prompt"`
	Options []string `json:"options,omitempty"`
	Default string   `json:"default,omitempty"`
}

// Validate 检查 Layout 是否适用于本转换器与这个文件：版本、格式、列数。
// 对不上就明确报错——静默忽略一份过期的 Layout，比报错更伤人。
func Validate(c Converter, l *Layout, columns int) error {
	if l == nil {
		return nil
	}
	if l.Version != LayoutVersion {
		return types.Errorf(types.CodeUsage, "表结构版本不支持：%d（当前 %d）", l.Version, LayoutVersion).
			WithHint("重新执行 inspect 生成新的结构文件")
	}
	if l.Format != "" && normalizeName(l.Format) != normalizeName(c.Name()) {
		return types.Errorf(types.CodeUsage, "表结构来自 %s，当前转换器是 %s", l.Format, c.Name()).
			WithHint("表结构与格式绑定，换格式后需要重新 inspect")
	}
	if columns > 0 && len(l.Columns) > 0 && len(l.Columns) != columns {
		return types.Errorf(types.CodeUsage, "表结构与文件不符：结构有 %d 列，文件有 %d 列",
			len(l.Columns), columns).
			WithHint("文件可能已更新，重新执行 inspect 确认结构")
	}
	return nil
}

// Convert 解析转换器、走完初始化回调，再执行一次转换。
//
// 这是对外的唯一入口：调用方不必知道注册表怎么工作，也不必自己调 Configure。
func Convert(o Options) (*Result, error) {
	c, err := Resolve(o.Converter, o.Input)
	if err != nil {
		return nil, err
	}
	if err := configure(c, o.ConverterOpts); err != nil {
		return nil, err
	}
	return c.Convert(o)
}

// Inspect 解析转换器并只读表结构（不写任何产物），供交互式确认流程反复调用。
func Inspect(o Options) (*Layout, error) {
	c, err := Resolve(o.Converter, o.Input)
	if err != nil {
		return nil, err
	}
	if err := configure(c, o.ConverterOpts); err != nil {
		return nil, err
	}
	return c.Inspect(o)
}

// Rehearse 解析转换器并做一次预演练（只读、不落产物）。
func Rehearse(o Options) (*Rehearsal, error) {
	c, err := Resolve(o.Converter, o.Input)
	if err != nil {
		return nil, err
	}
	if err := configure(c, o.ConverterOpts); err != nil {
		return nil, err
	}
	return c.Rehearse(o)
}

// configure 调用转换器的初始化回调，并把「配置写错了」统一翻译成 CodeUsage：
// 用户改的是配置文件，所以错误里要带上配置键路径，否则他不知道该改哪一节。
func configure(c Converter, opts map[string]any) error {
	if err := c.Configure(opts); err != nil {
		var te *types.Error
		if errors.As(err, &te) {
			return te
		}
		return types.Errorf(types.CodeUsage, "转换器 %s 配置有误：%v", c.Name(), err).
			WithHint("检查配置文件 convert.converter." + c.Name() + " 下的键名与取值")
	}
	return nil
}

// Resolve 把名字解析成一个**新实例**：空串与 auto 按输入扩展名挑，
// 显式名字则要求该转换器确实支持这个扩展名（写错名字或换错文件都要尽早报错）。
func Resolve(name, input string) (Converter, error) {
	n := normalizeName(name)
	ext := normalizeExt(filepath.Ext(input))
	if n == "" {
		newConverter, ok := ForExtension(ext)
		if !ok {
			return nil, types.Errorf(types.CodeUsage, "不支持的文件类型 %q", ext).
				WithHint("可用转换器：" + strings.Join(Names(), " / ") +
					"（支持：" + strings.Join(Extensions(), " ") + "）")
		}
		return newInstance(n, newConverter)
	}
	newConverter, ok := Lookup(n)
	if !ok {
		return nil, types.Errorf(types.CodeUsage, "unknown converter %q", name).
			WithHint("可用转换器：" + strings.Join(Names(), " / "))
	}
	c, err := newInstance(n, newConverter)
	if err != nil {
		return nil, err
	}
	if !Supports(c, ext) {
		return nil, types.Errorf(types.CodeUsage, "转换器 %s 不支持 %q", n, ext).
			WithHint("它支持：" + strings.Join(c.Extensions(), " "))
	}
	return c, nil
}

// Supports 判断转换器是否认这个扩展名（大小写不敏感）。
func Supports(c Converter, ext string) bool {
	ext = normalizeExt(ext)
	for _, e := range c.Extensions() {
		if normalizeExt(e) == ext {
			return true
		}
	}
	return false
}

func newInstance(name string, newConverter func() Converter) (Converter, error) {
	c := newConverter()
	if c == nil {
		return nil, types.Errorf(types.CodeGeneral, "converter %s 的工厂返回 nil", name)
	}
	return c, nil
}

// normalizeName 统一名字写法：小写、去空白；auto 归一为空串。
func normalizeName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == AutoConverterName {
		return ""
	}
	return n
}

// normalizeExt 统一扩展名写法：小写、去空白、补上前导点。
func normalizeExt(ext string) string {
	e := strings.ToLower(strings.TrimSpace(ext))
	if e == "" {
		return ""
	}
	if !strings.HasPrefix(e, ".") {
		e = "." + e
	}
	return e
}

// sortedKeys 返回 map 的键（升序），用于把「不支持哪些配置项」稳定地报出来。
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// LoadLayout 读取并校验一份已确认的表结构文件。
//
// 用 DisallowUnknownFields：结构文件是给人/agent 改的，把 header_row 拼成
// headerrow 之类的手误必须当场报错——静默忽略一个键，等于让用户以为自己改生效了。
func LoadLayout(path string) (*Layout, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, types.Errorf(types.CodeNotFound, "读取表结构失败：%v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var l Layout
	if err := dec.Decode(&l); err != nil {
		return nil, types.Errorf(types.CodeUsage, "表结构文件无法解析：%v", err).
			WithHint("对照 `dtool convert --inspect` 的输出字段名检查")
	}
	if l.Version != LayoutVersion {
		return nil, types.Errorf(types.CodeUsage, "表结构版本不支持：%d（当前 %d）", l.Version, LayoutVersion).
			WithHint("重新执行 inspect 生成新的结构文件")
	}
	return &l, nil
}
