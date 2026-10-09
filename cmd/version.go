package cmd

import (
	"github.com/dezhishen/dtool/internal/buildinfo"
	"github.com/spf13/cobra"
)

func newVersionCmd(info buildinfo.Info) *cobra.Command {
	var short, deps bool
	c := &cobra.Command{
		Use:   "version",
		Short: "显示版本与构建元数据（commit、分支、构建时间、CI 运行、Go 与平台）",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if short {
				_, err := c.OutOrStdout().Write([]byte(info.Version + "\n"))
				return err
			}
			if !deps {
				info.Deps = nil
			}
			return printJSON(c.OutOrStdout(), info)
		},
	}
	c.Flags().BoolVar(&short, "short", false, "只输出版本号")
	c.Flags().BoolVar(&deps, "deps", false, "附带编译进二进制的依赖模块及版本")
	return c
}
