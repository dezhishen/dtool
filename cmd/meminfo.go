package cmd

import (
	"os"
	"runtime"
	"strings"

	"github.com/dezhishen/dtool/internal/memguard"
	"github.com/dezhishen/dtool/internal/query"
	"github.com/spf13/cobra"
)

// newMemInfoCmd 输出内存预算的**来源与判定过程**：受限环境里「预检为什么没拦住」
// 用这一条命令就能回答——读到了什么上限、来自哪个 API（含原始字段）、预算是多少、
// 按当前输入会不会被拒绝。
func newMemInfoCmd() *cobra.Command {
	var srcs []string
	c := &cobra.Command{
		Use:   "meminfo",
		Short: "显示内存预算的来源、判定过程与装入预演（受限环境排查用）",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			detected := memguard.Detect() // 平台探测的原始结果（未加 15% 余量）
			mem := memguard.Budget(parsedMaxMemory)
			bindings := map[string]string{}
			for _, s := range srcs {
				alias, path, ok := strings.Cut(s, "=")
				if !ok {
					alias, path = s, s
				}
				bindings[alias] = path
			}
			usage := memguard.CurrentUsage()
			report := map[string]any{
				"platform": runtime.GOOS + "/" + runtime.GOARCH,
				"flags": map[string]any{
					"--max-memory 是否设置": parsedMaxMemory != nil,
					"--load-mode":       g.loadMode,
				},
				"env": map[string]any{
					"DTOOL_MAX_MEMORY": os.Getenv("DTOOL_MAX_MEMORY"),
					memguard.DebugEnv:  os.Getenv(memguard.DebugEnv),
					"DTOOL_NO_REAPER":  os.Getenv("DTOOL_NO_REAPER"),
				},
				"detected": map[string]any{
					"limit":           detected.Limit,
					"limit_human":     memguard.HumanSize(detected.Limit),
					"used":            detected.Used,
					"used_human":      memguard.HumanSize(detected.Used),
					"available":       detected.Available,
					"available_human": memguard.HumanSize(detected.Available),
					"source":          detected.Source,
				},
				"budget": map[string]any{
					"available":       mem.Available,
					"available_human": memguard.HumanSize(mem.Available),
					"source":          mem.Source,
					"describe":        mem.Describe(),
				},
				"guard": map[string]any{
					"watchdog":        mem.Available > 0,
					"threshold_human": memguard.HumanSize(mem.Available),
					"go_soft_limit":   mem.Available > 0,
				},
				"usage_now":       usage,
				"usage_now_human": memguard.HumanSize(usage),
				"job_object":      memguard.JobProbe(),
			}
			if len(bindings) > 0 {
				report["preview"] = query.PreviewLoad(g.loadMode, mem, bindings)
			}
			return printJSON(c.OutOrStdout(), report)
		},
	}
	c.Flags().StringArrayVar(&srcs, "source", nil, "预演某个数据源的预检结果：alias=文件，可重复")
	return c
}
