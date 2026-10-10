package cmd

import (
	"github.com/dezhishen/dtool/internal/pipeline"
	"github.com/spf13/cobra"
)

func newConvertCmd() *cobra.Command {
	var p pipeline.ConvertParams
	c := &cobra.Command{
		Use:   "convert",
		Short: "Excel → 数据集（JSON + JSON Schema）",
		Long: "Excel → 数据集（JSON + JSON Schema）。\n\n" +
			"转换始终是流式的两阶段解析：先逐行读单元格、累积列统计做类型推断，再按推断出的 Schema\n" +
			"逐行写出 data.json，峰值内存 ≈ 固定开销 + 文件体积 × 3，与行数无关，因此没有 --load-mode 开关\n" +
			"（那个参数选的是 JSON → 内存 SQLite 的装入方式，只对 query 生效）。",
		RunE: func(c *cobra.Command, _ []string) error {
			if err := requireFlags(c, "input"); err != nil {
				return err
			}
			env, err := newEnv(c.Context())
			if err != nil {
				return err
			}
			res, err := env.Convert(p, "")
			if err != nil {
				return err
			}
			return printJSON(c.OutOrStdout(), res)
		},
	}
	c.Flags().StringVar(&p.Input, "input", "", "Excel 文件路径（.xlsx）")
	c.Flags().StringVar(&p.Name, "name", "", "数据集名（默认取文件名）；之后可用 dataset:<name> 引用")
	c.Flags().StringVar(&p.Sheet, "sheet", "", "工作表名称，默认第一个")
	return c
}
