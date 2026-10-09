package cmd

import (
	"time"

	"github.com/dezhishen/dtool/internal/pipeline"
	"github.com/spf13/cobra"
)

func newPipelineCmd() *cobra.Command {
	var cfg pipeline.Config
	c := &cobra.Command{
		Use:   "pipeline",
		Short: "按需执行 convert →（可选）query →（可选）visualize；可只转换并持久化数据，SQL 中用 data 引用",
		RunE: func(c *cobra.Command, _ []string) error {
			if cfg.Chart != "" && cfg.Chart != "table" {
				if err := requireFlags(c, "x", "y"); err != nil {
					return err
				}
			}
			applyFont(c, &cfg.Font)
			env, err := newEnv(c.Context())
			if err != nil {
				return err
			}
			res, err := env.Pipeline(cfg)
			if err != nil {
				return err
			}
			return printJSON(c.OutOrStdout(), res)
		},
	}
	f := c.Flags()
	f.StringVar(&cfg.Excel, "excel", "", "Excel 文件路径")
	f.StringVar(&cfg.Input, "input", "", "复用已持久化的数据：文件 / dataset:<name> / action:<id> / latest:convert（与 --excel 二选一）")
	f.StringVar(&cfg.Name, "name", "", "数据集名（默认取文件名）")
	f.StringVar(&cfg.Sheet, "sheet", "", "工作表")
	f.StringVar(&cfg.SQL, "sql", "", `SQL（可省略，省略则只转换），例如 SELECT region, SUM(amount) AS total FROM data GROUP BY region`)
	f.StringVar(&cfg.Chart, "chart", "", "图表类型 bar / line / pie / table，留空则不出图")
	f.StringVar(&cfg.X, "x", "", "X 轴字段")
	f.StringVar(&cfg.Y, "y", "", "Y 轴字段")
	f.StringVar(&cfg.Title, "title", "", "图表标题")
	f.StringVar(&cfg.Format, "format", "png", "png / svg")
	f.StringVar(&cfg.Font, "font", "", "TTF 字体路径")
	f.StringVar(&cfg.Output, "output", "", "图表输出文件")
	f.IntVar(&cfg.MaxRows, "max-rows", 10000, "结果行数上限")
	f.DurationVar(&cfg.Timeout, "timeout", 60*time.Second, "查询超时")
	f.BoolVar(&cfg.Sandbox, "sandbox", true, "SQL 沙箱")
	return c
}
