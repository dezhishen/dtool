package cmd

import (
	"github.com/dezhishen/dtool/internal/pipeline"
	"github.com/dezhishen/dtool/internal/visualize"
	"github.com/spf13/cobra"
)

func newVisualizeCmd() *cobra.Command {
	var p pipeline.VisualizeParams
	var yFlag string
	c := &cobra.Command{
		Use:   "visualize",
		Short: "结果 → 图表（bar/line/pie，PNG/SVG）或表格（Markdown）",
		RunE: func(c *cobra.Command, _ []string) error {
			if err := requireFlags(c, "input", "type"); err != nil {
				return err
			}
			if p.Type != "table" {
				if err := requireFlags(c, "x", "y"); err != nil {
					return err
				}
			}
			p.From = g.from
			p.Y = visualize.ParseY(yFlag)
			applyFont(c, &p.Font)
			env, err := newEnv(c.Context())
			if err != nil {
				return err
			}
			res, err := env.Visualize(p, "")
			if err != nil {
				return err
			}
			if strongPrivacy() {
				return printDelivery(c, res.ActionID, artifactItem{"chart", res.File})
			}
			return printJSON(c.OutOrStdout(), res)
		},
	}
	f := c.Flags()
	f.StringVar(&p.Input, "input", "", "数据来源：文件 / dataset:<name> / action:<id> / latest:<type>")
	f.StringVar(&p.Type, "type", "", "bar / line / pie / table")
	f.StringVar(&p.X, "x", "", "X 轴字段")
	f.StringVar(&yFlag, "y", "", "Y 轴数值列，多个用逗号分隔：sales,profit")
	f.StringVar(&p.GroupBy, "group-by", "", "按该字段拆分多系列（bar / line）：如 region × quarter")
	f.StringVar(&p.Missing, "missing", "zero", "缺失组合处理：zero（补 0，默认）/ gap（置空，line 断线）")
	f.StringVar(&p.Title, "title", "", "标题")
	f.StringVar(&p.Output, "output", "", "输出文件，默认写入工作区产物目录")
	f.StringVar(&p.Format, "format", "png", "png / svg；--type table 时可用 md / xlsx")
	f.StringVar(&p.Font, "font", "", "字体文件（.ttf/.ttc）；缺省时依次用配置文件 font、DTOOL_FONT、系统中文字体")
	return c
}
