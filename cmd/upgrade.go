package cmd

import (
	"github.com/dezhishen/dtool/internal/updater"
	"github.com/dezhishen/dtool/pkg/types"
	"github.com/spf13/cobra"
)

// runCheckUpdate 对应 `dtool --update [--pre]`：只检查，不改动任何文件。
func runCheckUpdate(c *cobra.Command, version string, pre bool) error {
	res, err := updater.New(version).Check(c.Context(), pre)
	if err != nil {
		return err
	}
	return printJSON(c.OutOrStdout(), res)
}

func newUpgradeCmd(version string) *cobra.Command {
	var target string
	var pre bool
	c := &cobra.Command{
		Use:   "upgrade",
		Short: "升级 dtool 到最新版或指定版本（校验 sha256，Windows 下安全替换运行中的文件）",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if target != "" && pre {
				return types.Errorf(types.CodeUsage, "--version 与 --pre 不能同时使用").
					WithHint("预览版请直接指定，如 --version 1.2.0-preview.1")
			}
			res, err := updater.New(version).Upgrade(c.Context(), updater.UpgradeOptions{Version: target, Pre: pre})
			if err != nil {
				return err
			}
			return printJSON(c.OutOrStdout(), res)
		},
	}
	c.Flags().StringVar(&target, "version", "", "目标版本，如 v1.2.0 或 1.2.0-preview.1（可降级）")
	c.Flags().BoolVar(&pre, "pre", false, "最新版包含预览版")
	return c
}
