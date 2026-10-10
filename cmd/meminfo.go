package cmd

import (
	"fmt"
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
// warnings 把「探测结果是否可信」翻成人话：受限环境里最怕的不是读不到上限，
// 而是**读到 0 却当成没有上限**——预检与看门狗会一起失效，用户只在进程被系统
// 拒绝分配时看到一句 fatal error: out of memory。
func warnings(job memguard.JobInfo, usage uint64, mem memguard.Memory) []string {
	var out []string
	if runtime.GOOS == "windows" {
		switch {
		case job.LimitUnreadable:
			out = append(out, fmt.Sprintf(
				"看起来在 Job Object 里但读不到上限（QueryInformationJobObject last_error=%d），"+
					"预算回落到系统可用内存；沙箱/CI 里请用 --max-memory 显式指定上限", job.QueryErr))
		case job.UsedLimit > 0:
			out = append(out, fmt.Sprintf("Job Object 上限 %s 已生效（来源：%s），预算按它计算",
				memguard.HumanSize(job.UsedLimit), job.UsedLimitSource))
		}
	}
	if mem.Available > 0 && usage > mem.Available {
		out = append(out, fmt.Sprintf("当前已用 %s 已超过预算 %s，装载会被预检拒绝",
			memguard.HumanSize(usage), memguard.HumanSize(mem.Available)))
	}
	return out
}

func newMemInfoCmd() *cobra.Command {
	var srcs []string
	c := &cobra.Command{
		Use:   "meminfo",
		Short: "显示内存预算的来源、判定过程与装入预演（受限环境排查用）",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			detected := memguard.Detect() // 平台探测的原始结果（未加 15% 余量）
			mem := memguard.Budget(parsedMaxMemory)
			job := memguard.JobProbe()
			bindings := map[string]string{}
			for _, s := range srcs {
				alias, path, ok := strings.Cut(s, "=")
				if !ok {
					alias, path = s, s
				}
				bindings[alias] = path
			}
			usage := memguard.CurrentUsage()
			warns := warnings(job, usage, mem)
			if warns == nil {
				warns = []string{} // 空数组比 null 好：调用方不用为空值写特例
			}
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
				"job_object":      job,
				"warnings":        warns,
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
