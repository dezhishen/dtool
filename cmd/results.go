package cmd

import (
	"strconv"
	"strings"
	"time"

	"github.com/dezhishen/dtool/internal/action"
	"github.com/dezhishen/dtool/pkg/types"
	"github.com/spf13/cobra"
)

func newResultsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "results",
		Short: "管理 Action 产出的结果集（只读元数据；GC 默认 dry-run）",
		Long: "枚举和统计 Action 的 output.files。结果集是 Action 的产物，不是独立数据集；\n" +
			"convert 的持久化数据集仍由 `dtool datasets` 管理。\n" +
			"强隐私下只输出路径/大小/行数/校验和等元数据，不读取或打印内容。",
	}
	var typ, status string
	var hash bool

	list := &cobra.Command{
		Use: "list", Short: "列出结果集（最新在前）",
		RunE: func(c *cobra.Command, _ []string) error {
			t, err := resultType(typ)
			if err != nil {
				return err
			}
			rec, _, err := newRecorder()
			if err != nil {
				return err
			}
			sets, err := rec.CollectResults(action.ResultQuery{Type: t, Status: status, Hash: hash})
			if err != nil {
				return err
			}
			if sets == nil {
				sets = []action.ResultSet{}
			}
			return printJSON(c.OutOrStdout(), map[string]any{"results": sets, "total": len(sets)})
		},
	}
	list.Flags().StringVar(&typ, "type", "all", "结果类型：query / visualize / all（convert 产物由 datasets 管理）")
	list.Flags().StringVar(&status, "status", "", "按 Action 状态过滤：success / failed / running / stale")
	list.Flags().BoolVar(&hash, "hash", false, "同时计算 sha256（大文件会变慢）")

	stat := &cobra.Command{
		Use: "stat", Short: "统计结果集数量/大小/行数/缺失/外部文件",
		RunE: func(c *cobra.Command, _ []string) error {
			t, err := resultType(typ)
			if err != nil {
				return err
			}
			rec, _, err := newRecorder()
			if err != nil {
				return err
			}
			sets, err := rec.CollectResults(action.ResultQuery{Type: t, Status: status})
			if err != nil {
				return err
			}
			return printJSON(c.OutOrStdout(), action.ResultStatistics(sets))
		},
	}
	stat.Flags().StringVar(&typ, "type", "all", "结果类型：query / visualize / all")
	stat.Flags().StringVar(&status, "status", "", "按 Action 状态过滤")

	verify := &cobra.Command{
		Use: "verify", Short: "校验结果文件是否存在、可读并计算 sha256",
		RunE: func(c *cobra.Command, _ []string) error {
			t, err := resultType(typ)
			if err != nil {
				return err
			}
			rec, _, err := newRecorder()
			if err != nil {
				return err
			}
			sets, err := rec.CollectResults(action.ResultQuery{Type: t, Status: status, Hash: true})
			if err != nil {
				return err
			}
			return printJSON(c.OutOrStdout(), action.VerifyResults(sets))
		},
	}
	verify.Flags().StringVar(&typ, "type", "all", "结果类型：query / visualize / all")
	verify.Flags().StringVar(&status, "status", "", "按 Action 状态过滤")

	var olderThan string
	var keepLast int
	var apply, force bool
	gc := &cobra.Command{
		Use: "gc", Short: "清理过期结果文件（默认 dry-run；只动工作区 outputs/）",
		Long: "默认只计划不删除；加 --apply 才真正删除。\n" +
			"永远不动：外部 --output 文件、数据集、仍被其他 Action 引用的结果。\n" +
			"默认保留每类最近 5 个，且只删除早于 --older-than 的结果。",
		RunE: func(c *cobra.Command, _ []string) error {
			t, err := resultType(typ)
			if err != nil {
				return err
			}
			age, err := parseAge(olderThan)
			if err != nil {
				return err
			}
			if keepLast < 0 {
				return types.Errorf(types.CodeUsage, "--keep-last 不能为负数")
			}
			rec, _, err := newRecorder()
			if err != nil {
				return err
			}
			rep, err := rec.GCResults(action.GCOptions{
				Type: t, Status: status, OlderThan: age, KeepLast: keepLast, Apply: apply, Force: force,
			})
			if err != nil {
				return err
			}
			return printJSON(c.OutOrStdout(), rep)
		},
	}
	gc.Flags().StringVar(&typ, "type", "all", "结果类型：query / visualize / all")
	gc.Flags().StringVar(&status, "status", "success", "只清理该状态的 Action")
	gc.Flags().StringVar(&olderThan, "older-than", "30d", "只清理早于该时长的结果，如 30d / 24h / 0")
	gc.Flags().IntVar(&keepLast, "keep-last", 5, "每类至少保留最近 N 个结果")
	gc.Flags().BoolVar(&apply, "apply", false, "真正删除（默认只 dry-run）")
	gc.Flags().BoolVar(&force, "force", false, "忽略引用保护强制删除（仍不动外部文件和数据集）")

	c.AddCommand(list, stat, verify, gc)
	return c
}

func resultType(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "all":
		return "all", nil
	case "query", "visualize":
		return strings.ToLower(strings.TrimSpace(s)), nil
	default:
		return "", types.Errorf(types.CodeUsage, "results 只支持 query / visualize / all").
			WithHint("convert 的产物是数据集，请用 `dtool datasets list/show/delete` 管理")
	}
}

func parseAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, types.Errorf(types.CodeUsage, "--older-than 不能为空")
	}
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n < 0 {
			return 0, types.Errorf(types.CodeUsage, "invalid --older-than %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, types.Errorf(types.CodeUsage, "invalid --older-than %q", s).
			WithHint("用 30d、24h、90m 或 0")
	}
	return d, nil
}
