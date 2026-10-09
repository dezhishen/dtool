package cmd

import (
	"os"
	"path/filepath"
	"time"

	"github.com/dezhishen/dtool/internal/action"
	"github.com/dezhishen/dtool/internal/workspace"
	"github.com/dezhishen/dtool/pkg/types"
	"github.com/spf13/cobra"
)

func newRecorder() (*action.Recorder, *workspace.Workspace, error) {
	ws, err := openWorkspace()
	if err != nil {
		return nil, nil, err
	}
	return &action.Recorder{WS: ws}, ws, nil
}

func newActionsCmd() *cobra.Command {
	c := &cobra.Command{Use: "actions", Short: "查询、追踪、补充 Action"}

	var f action.Filter
	list := &cobra.Command{
		Use: "list", Short: "列出 Action（最新在前）",
		RunE: func(c *cobra.Command, _ []string) error {
			rec, _, err := newRecorder()
			if err != nil {
				return err
			}
			entries, total, err := rec.List(f)
			if err != nil {
				return err
			}
			if entries == nil {
				entries = []action.IndexEntry{}
			}
			return printJSON(c.OutOrStdout(), map[string]any{"actions": entries, "total": total})
		},
	}
	list.Flags().IntVar(&f.Limit, "limit", 0, "最多返回条数")
	list.Flags().StringVar(&f.Type, "type", "", "按类型过滤")
	list.Flags().StringVar(&f.Status, "status", "", "按状态过滤: success / failed / running / stale")

	show := &cobra.Command{
		Use: "show <id>", Short: "查看 Action 完整详情", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			rec, _, err := newRecorder()
			if err != nil {
				return err
			}
			a, err := rec.Get(args[0])
			if err != nil {
				return err
			}
			return printJSON(c.OutOrStdout(), a)
		},
	}

	output := &cobra.Command{
		Use: "output <id>", Short: "输出 Action 主产物的完整内容", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			rec, ws, err := newRecorder()
			if err != nil {
				return err
			}
			r, err := rec.Resolve("action:" + args[0])
			if err != nil {
				return err
			}
			data, err := os.ReadFile(r.Path)
			if err != nil {
				return types.Errorf(types.CodeNotFound, "output missing: %s", ws.Rel(r.Path))
			}
			_, err = c.OutOrStdout().Write(data)
			return err
		},
	}

	trace := &cobra.Command{
		Use: "trace <id>", Short: "查看派生/演进链路", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			rec, _, err := newRecorder()
			if err != nil {
				return err
			}
			up, down, err := rec.Trace(args[0])
			if err != nil {
				return err
			}
			if up == nil {
				up = []action.IndexEntry{}
			}
			if down == nil {
				down = []action.IndexEntry{}
			}
			return printJSON(c.OutOrStdout(), map[string]any{"id": args[0], "upstream": up, "downstream": down})
		},
	}

	var text, by string
	annotate := &cobra.Command{
		Use: "annotate <id>", Short: "给 Action 追加注释", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if err := requireFlags(c, "text"); err != nil {
				return err
			}
			rec, _, err := newRecorder()
			if err != nil {
				return err
			}
			a, err := rec.Annotate(args[0], text, by)
			if err != nil {
				return err
			}
			return printJSON(c.OutOrStdout(), map[string]any{"success": true, "action_id": a.ID, "annotations": a.Annotations})
		},
	}
	annotate.Flags().StringVar(&text, "text", "", "注释内容")
	annotate.Flags().StringVar(&by, "by", "user", "注释者: user / ai-agent")

	var exportOut string
	var exportLimit int
	export := &cobra.Command{
		Use: "export", Short: "导出所有 Action 为单个 JSON",
		RunE: func(c *cobra.Command, _ []string) error {
			rec, ws, err := newRecorder()
			if err != nil {
				return err
			}
			all, err := rec.All()
			if err != nil {
				return err
			}
			if exportLimit > 0 && len(all) > exportLimit {
				all = all[len(all)-exportLimit:]
			}
			path := exportOut
			if path == "" {
				path = filepath.Join(ws.Root, "actions_dump.json")
			}
			doc := map[string]any{"exported_at": time.Now().UTC(), "workspace_id": ws.Info.WorkspaceID, "actions": all}
			if err := workspace.WriteJSONAtomic(path, doc); err != nil {
				return err
			}
			return printJSON(c.OutOrStdout(), map[string]any{"success": true, "file": ws.Rel(path), "count": len(all)})
		},
	}
	export.Flags().StringVar(&exportOut, "output", "", "输出文件，默认 .dtool/actions_dump.json")
	export.Flags().IntVar(&exportLimit, "limit", 0, "仅导出最近 N 条")

	reindex := &cobra.Command{
		Use: "reindex", Short: "从 actions/ 重建 index.json",
		RunE: func(c *cobra.Command, _ []string) error {
			rec, _, err := newRecorder()
			if err != nil {
				return err
			}
			n, err := rec.Reindex()
			if err != nil {
				return err
			}
			return printJSON(c.OutOrStdout(), map[string]any{"success": true, "count": n})
		},
	}

	c.AddCommand(list, show, output, trace, annotate, export, reindex)
	return c
}
