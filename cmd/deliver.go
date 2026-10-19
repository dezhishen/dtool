package cmd

import (
	"path/filepath"
	"strings"

	"github.com/dezhishen/dtool/pkg/types"
	"github.com/spf13/cobra"
)

// deliver 只返回产物清单：文件在哪、多大、sha256、怎么发。它不读内容，
// 因此在强隐私模式下也允许使用。
func newDeliverCmd() *cobra.Command {
	c := &cobra.Command{
		Use: "deliver <id>", Short: "输出 Action 产物的交付清单（只给位置与发送方式，不读内容）",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			rec, _, err := newRecorder()
			if err != nil {
				return err
			}
			a, err := rec.Get(args[0])
			if err != nil {
				return err
			}
			if a.Output == nil || len(a.Output.Files) == 0 {
				return types.Errorf(types.CodeNotFound, "action %s 没有产物文件", args[0]).
					WithHint("用 `dtool actions show <id>` 查看 Action 详情")
			}
			items := make([]artifactItem, 0, len(a.Output.Files))
			for i, f := range a.Output.Files {
				role := "artifact"
				switch {
				case strings.HasSuffix(f, ".schema.json"):
					role = "schema"
				case i == 0:
					role = "output"
				case filepath.Ext(f) == ".png" || filepath.Ext(f) == ".svg":
					role = "chart"
				}
				items = append(items, artifactItem{role, f})
			}
			return printDelivery(c, a.ID, items...)
		},
	}
	return c
}
