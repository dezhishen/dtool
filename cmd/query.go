package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dezhishen/dtool/internal/formatter"
	"github.com/dezhishen/dtool/internal/pipeline"
	"github.com/dezhishen/dtool/pkg/types"
	"github.com/spf13/cobra"
)

func parseSources(list []string) (map[string]string, error) {
	m := map[string]string{}
	for _, s := range list {
		alias, ref, ok := strings.Cut(s, "=")
		if !ok || alias == "" || ref == "" {
			return nil, types.Errorf(types.CodeUsage, "invalid --source %q, want alias=ref", s)
		}
		m[alias] = ref
	}
	return m, nil
}

func newQueryCmd() *cobra.Command {
	var p pipeline.QueryParams
	var sources []string
	var rowsFlag bool
	c := &cobra.Command{
		Use:   "query",
		Short: "用 SQL 查询数据集或 JSON 文件（内存 SQLite）",
		RunE: func(c *cobra.Command, _ []string) error {
			if err := requireFlags(c, "sql"); err != nil {
				return err
			}
			if strongPrivacy() {
				if rowsFlag {
					return forbidStrong("把结果行输出到 stdout（--rows）")
				}
				if p.Format != "json" && p.Output == "" {
					return forbidStrong(fmt.Sprintf("把 %s 数据直接输出到 stdout", p.Format))
				}
			}
			var err error
			if p.Sources, err = parseSources(sources); err != nil {
				return err
			}
			p.From = g.from
			env, err := newEnv(c.Context())
			if err != nil {
				return err
			}
			res, err := env.Query(p, "")
			if err != nil {
				return err
			}
			if strongPrivacy() {
				var items []artifactItem
				addArtifact(&items, "result", res.ResultFile)
				addArtifact(&items, "output", res.OutputFile)
				return printDelivery(c, res.ActionID, items...)
			}
			if p.Format == "" || p.Format == "json" || p.Output != "" {
				if !rowsFlag {
					res.Rows = nil // 默认只输出摘要；明细已在 result_file
				}
				return printJSON(c.OutOrStdout(), res)
			}
			if err := formatter.Write(c.OutOrStdout(), p.Format, res); err != nil {
				return err
			}
			if res.ActionID != "" {
				fmt.Fprintln(os.Stderr, "action_id:", res.ActionID)
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringVar(&p.SQL, "sql", "", "SQL 语句；表名用双引号包裹文件路径，或用 --source 别名")
	f.StringArrayVar(&sources, "source", nil, "数据源绑定 alias=ref，ref 为文件 / dataset:<name> / action:<id> / latest:<type>，可重复")
	f.StringVar(&p.Format, "format", "json", "输出格式: json / csv / markdown / table / xlsx（xlsx 需配合 --output）")
	f.StringVar(&p.Output, "output", "", "输出文件路径")
	f.BoolVar(&rowsFlag, "rows", false, "在 stdout 输出完整结果行（默认只输出摘要，数据已写入 result_file）")
	f.IntVar(&p.MaxRows, "max-rows", 10000, "结果行数上限，0 表示不限")
	f.DurationVar(&p.Timeout, "timeout", 60*time.Second, "查询超时")
	f.BoolVar(&p.Sandbox, "sandbox", true, "沙箱：仅允许单条 SELECT，且只读工作区/当前目录/--source 文件")
	return c
}
