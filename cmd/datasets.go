package cmd

import (
	"encoding/json"
	"os"

	"github.com/dezhishen/dtool/internal/dataset"
	"github.com/dezhishen/dtool/pkg/types"
	"github.com/spf13/cobra"
)

func newDatasetsCmd() *cobra.Command {
	store := func() (*dataset.Store, error) {
		ws, err := openWorkspace()
		if err != nil {
			return nil, err
		}
		return &dataset.Store{WS: ws}, nil
	}
	c := &cobra.Command{Use: "datasets", Short: "管理独立于 Action 的数据集（convert 的产物）"}

	list := &cobra.Command{
		Use: "list", Short: "列出所有数据集",
		RunE: func(c *cobra.Command, _ []string) error {
			ds, err := store()
			if err != nil {
				return err
			}
			items, err := ds.List()
			if err != nil {
				return err
			}
			return printJSON(c.OutOrStdout(), map[string]any{"datasets": items, "total": len(items)})
		},
	}

	show := &cobra.Command{
		Use: "show <name>", Short: "查看数据集：位置、Schema 与数据预览", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ds, err := store()
			if err != nil {
				return err
			}
			info, err := ds.Info(args[0])
			if err != nil {
				return err
			}
			doc := map[string]any{"dataset": info}
			var rows []types.Row
			data, err := os.ReadFile(ds.Path(args[0]))
			if err == nil {
				err = json.Unmarshal(data, &rows)
			}
			if err != nil {
				return types.Errorf(types.CodeExec, "read dataset %s: %v", args[0], err)
			}
			n := g.previewRows
			if n <= 0 {
				n = 20
			}
			doc["preview_truncated"] = len(rows) > n
			if len(rows) > n {
				rows = rows[:n]
			}
			doc["preview"] = rows
			var schema any
			if b, err := os.ReadFile(ds.SchemaPath(args[0])); err == nil && json.Unmarshal(b, &schema) == nil {
				doc["schema"] = schema
			}
			return printJSON(c.OutOrStdout(), doc)
		},
	}

	del := &cobra.Command{
		Use: "delete <name>", Short: "删除数据集（已有 Action 记录保留，但不再能引用它）", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ds, err := store()
			if err != nil {
				return err
			}
			if err := ds.Delete(args[0]); err != nil {
				return err
			}
			return printJSON(c.OutOrStdout(), map[string]any{"success": true, "name": args[0]})
		},
	}

	c.AddCommand(list, show, del)
	return c
}
