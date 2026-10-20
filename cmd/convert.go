package cmd

import (
	"strings"

	"github.com/dezhishen/dtool/internal/converter"
	"github.com/dezhishen/dtool/internal/pipeline"
	"github.com/spf13/cobra"
)

func newConvertCmd() *cobra.Command {
	var p pipeline.ConvertParams
	var (
		inspect    bool
		rehearse   bool
		layoutPath string
	)
	c := &cobra.Command{
		Use:   "convert",
		Short: "表格文件 → 数据集（JSON + JSON Schema），当前支持 .xlsx",
		Long: "表格文件 → 数据集（JSON + JSON Schema），当前支持 .xlsx。\n\n" +
			"转换始终是流式的两阶段解析：先逐行读单元格、累积列统计做类型推断，再按推断出的 Schema\n" +
			"逐行写出 data.json，峰值内存 ≈ 固定开销 + 文件体积 × 3，与行数无关，因此没有 --load-mode 开关\n" +
			"（那个参数选的是 JSON → 查询库的装入方式，只对 query 生效）。\n\n" +
			"结构提取往往是反复的：先 --inspect 看结构（工作表、表头行、每列类型与置信度、待确认问题），\n" +
			"把人/agent 的确认结果写进 Layout JSON，再用 --layout 回传。带上 --layout 的转换不再自动探测，\n" +
			"结构对不上会直接报错（而不是悄悄换一种猜法）。",
		RunE: func(c *cobra.Command, _ []string) error {
			if err := requireFlags(c, "input"); err != nil {
				return err
			}
			if layoutPath != "" {
				l, err := converter.LoadLayout(layoutPath)
				if err != nil {
					return err
				}
				p.Layout = l
			}
			if rehearse {
				// 预演练：把每个待确认点展开成候选（表头行/工作表/列类型）与各自影响，
				// 同样只读，可以反复跑。优先级高于 --inspect：一次能看到更多。
				r, err := converter.Rehearse(converter.Options{Ctx: c.Context(), Converter: p.Converter,
					// 只有 normal 隐私才把候选行的单元格原文放进标签（强隐私下只报结构）。
					IncludeSamples: !strongPrivacy(),
					Layout:         p.Layout, Input: p.Input, Sheet: p.Sheet})
				if err != nil {
					return err
				}
				return printJSON(c.OutOrStdout(), r)
			}
			if inspect {
				// 只读结构：不建工作区、不记 Action、不写产物，因此可以反复跑。
				l, err := converter.Inspect(converter.Options{Ctx: c.Context(), Converter: p.Converter,
					Layout: p.Layout, Input: p.Input, Sheet: p.Sheet})
				if err != nil {
					return err
				}
				return printJSON(c.OutOrStdout(), l)
			}
			env, err := newEnv(c.Context())
			if err != nil {
				return err
			}
			res, err := env.Convert(p, "")
			if err != nil {
				return err
			}
			if strongPrivacy() {
				var items []artifactItem
				addArtifact(&items, "data", res.DataFile)
				addArtifact(&items, "schema", res.SchemaFile)
				return printDelivery(c, res.ActionID, items...)
			}
			return printJSON(c.OutOrStdout(), res)
		},
	}
	c.Flags().StringVar(&p.Input, "input", "", "表格文件路径（.xlsx）")
	c.Flags().StringVar(&p.Name, "name", "", "数据集名（默认取文件名）；之后可用 dataset:<name> 引用")
	c.Flags().StringVar(&p.Sheet, "sheet", "", "工作表名称，默认第一个")
	c.Flags().StringVar(&p.Converter, "converter", converter.AutoConverterName,
		"转换器：auto 按扩展名选择，或指定已注册的名字（"+strings.Join(converter.Names(), " / ")+"）")
	c.Flags().BoolVar(&inspect, "inspect", false, "只输出表结构（Layout JSON），不写数据集；可反复执行")
	c.Flags().BoolVar(&rehearse, "rehearse", false, "预演练：列出表头行/工作表/列类型的候选方案与影响，不写数据集")
	c.Flags().StringVar(&layoutPath, "layout", "", "使用已确认的表结构文件（JSON），不再自动探测")
	return c
}
