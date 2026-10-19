package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dezhishen/dtool/internal/dataset"
	"github.com/dezhishen/dtool/internal/pipeline"
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
		Use: "show <name>", Short: "查看数据集：位置与结构（取值走 datasets values）", Args: cobra.ExactArgs(1),
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
			var schema map[string]any
			if b, err := os.ReadFile(ds.SchemaPath(args[0])); err == nil && json.Unmarshal(b, &schema) == nil {
				// 兼容旧数据集：即使 schema 文件里还留着原值字段，展示时也一律剥掉。
				if cols, ok := schema["columns"].([]any); ok {
					for _, c := range cols {
						if m, ok := c.(map[string]any); ok {
							delete(m, "enum")
							delete(m, "samples")
							delete(m, "min")
							delete(m, "max")
						}
					}
				}
				doc["schema"] = schema
			}
			return printJSON(c.OutOrStdout(), doc)
		},
	}

	var column string
	var limit int
	var output string
	values := &cobra.Command{
		Use: "values <name> --column <col>", Short: "数据动作：读取某列的去重取值", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if err := requireFlags(c, "column"); err != nil {
				return err
			}
			if err := forbidStrong("执行 datasets values（读取具体取值）"); err != nil {
				return err
			}
			if limit <= 0 {
				return types.Errorf(types.CodeUsage, "--limit 必须大于 0")
			}
			name, col := args[0], column
			esc := func(s string) string { return strings.ReplaceAll(s, `"`, `""`) }
			sql := fmt.Sprintf(`SELECT DISTINCT "%s" AS value FROM "%s" WHERE "%s" IS NOT NULL ORDER BY value LIMIT %d`,
				esc(col), esc(name), esc(col), limit+1)
			env, err := newEnv(c.Context())
			if err != nil {
				return err
			}
			res, err := env.Query(pipeline.QueryParams{
				SQL: sql, Format: "json", Output: output, MaxRows: limit + 1,
				Timeout: 60 * time.Second, Sandbox: true,
				Extra: map[string]any{"purpose": "distinct_values", "dataset": name, "column": col, "limit": limit},
			}, "")
			if err != nil {
				return err
			}
			vals := make([]any, 0, len(res.Rows))
			for _, r := range res.Rows {
				if v, ok := r.Get("value"); ok {
					vals = append(vals, v)
				}
			}
			truncated := len(vals) > limit
			if truncated {
				vals = vals[:limit]
			}
			doc := map[string]any{"success": true, "dataset": name, "column": col,
				"values": vals, "returned": len(vals), "truncated": truncated,
				"result_file": res.ResultFile, "action_id": res.ActionID}
			if res.OutputFile != "" {
				doc["output_file"] = res.OutputFile
			}
			return printJSON(c.OutOrStdout(), doc)
		},
	}
	values.Flags().StringVar(&column, "column", "", "要读取取值的列名")
	values.Flags().IntVar(&limit, "limit", 20, "最多返回多少个去重值")
	values.Flags().StringVar(&output, "output", "", "同时写一份 JSON 结果到该文件")

	var resultRef string
	create := &cobra.Command{
		Use: "create <name> --result <ref>", Short: "数据动作：把 query 结果集提升为独立数据集", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if err := requireFlags(c, "result"); err != nil {
				return err
			}
			if err := forbidStrong("从结果集生成数据集"); err != nil {
				return err
			}
			env, err := newEnv(c.Context())
			if err != nil {
				return err
			}
			res, err := env.DatasetFromResult(pipeline.DatasetFromResultParams{Name: args[0], From: resultRef}, "")
			if err != nil {
				return err
			}
			return printJSON(c.OutOrStdout(), res)
		},
	}
	create.Flags().StringVar(&resultRef, "result", "", "结果集引用：action:<id> / latest:query")

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

	c.AddCommand(list, show, values, create, del)
	return c
}
