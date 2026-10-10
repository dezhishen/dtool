package cmd

import (
	"github.com/dezhishen/dtool/internal/action"
	"github.com/dezhishen/dtool/internal/workspace"
	"github.com/spf13/cobra"
)

// newReapCmd 是看护进程的入口（隐藏命令）：由 action.StartReaper 拉起，
// 等主进程消失后把它遗留的 running 收敛为 stale。
func newReapCmd() *cobra.Command {
	var wsDir string
	var pid int
	c := &cobra.Command{
		Use:    action.ReapArg,
		Hidden: true,
		Short:  "内部：看护主进程，主进程被强杀后收敛 Action 状态",
		Args:   cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if pid <= 0 {
				return nil
			}
			// 直接开工作区：不走 openWorkspace，避免入口那轮回收抢先下手。
			ws, err := workspace.Open(wsDir)
			if err != nil {
				return nil // 看护进程永不报错：它失败不该影响任何人
			}
			action.ReapWatch(ws, pid, action.ReapPoll, action.ReapMaxWait)
			return nil
		},
	}
	c.Flags().StringVar(&wsDir, "workspace", ".dtool", "工作区目录")
	c.Flags().IntVar(&pid, "pid", 0, "要守望的主进程 pid")
	return c
}
