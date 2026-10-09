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
