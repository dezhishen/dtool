package action

import (
	"fmt"

	"github.com/dezhishen/dtool/internal/workspace"
	"github.com/dezhishen/dtool/pkg/types"
)

// SyncReport 描述一次状态收敛的结果。
type SyncReport struct {
	// Scanned 是索引里的条目总数。
	Scanned int `json:"scanned"`
	// Stale 是本次被收敛为 stale 的条数（进程已消失的 running）。
	Stale int `json:"stale"`
	// StaleIDs 是本次收敛的 Action ID。
	StaleIDs []string `json:"stale_ids,omitempty"`
	// Running 是进程仍活着、确实还在跑的任务。
	Running []IndexEntry `json:"running,omitempty"`
}

// Sync 收敛 Action 状态并报告结果：进程已死的 running -> stale（落盘），
// 仍活着的 running 原样列出。`dtool actions sync` 就是它的出口。
func (r *Recorder) Sync() (*SyncReport, error) {
	// 与 save/annotate 共用工作区锁：回收会改写 Action 文件与 index.json，
	// 不能与别的进程的写入交错（否则可能出现「后写覆盖前写」丢条目）。
	unlock, err := r.WS.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	idx := r.loadIndex()
	rep := &SyncReport{Scanned: len(idx.Actions)}
	for i := range idx.Actions {
		e := idx.Actions[i]
		if e.Status != StatusRunning {
			continue
		}
		if pidAlive(e.Pid) {
			rep.Running = append(rep.Running, e)
			continue
		}
		a, err := r.readRaw(e.ID)
		if err != nil {
			continue // 文件已被删/损坏：跳过，不因此让整个命令失败
		}
		if a.Status != StatusRunning || pidAlive(a.Pid) {
			continue // 期间被别处补上了结束状态
		}
		markStale(a)
		if err := workspace.WriteJSONAtomic(r.WS.ActionPath(a.ID), a); err != nil {
			return rep, err
		}
		idx.Actions[i] = entryOf(a)
		rep.Stale++
		rep.StaleIDs = append(rep.StaleIDs, a.ID)
	}
	if rep.Stale > 0 {
		if err := workspace.WriteJSONAtomic(r.WS.IndexPath(), idx); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// Reconcile 收敛被中断的 Action，返回收敛条数；命令入口用的就是它。
func (r *Recorder) Reconcile() (int, error) {
	rep, err := r.Sync()
	if err != nil {
		return 0, err
	}
	return rep.Stale, nil
}

// markStale 把被中断的 Action 标成 stale，并写清「进程没了、结果未知」。
// 不臆造结束时间：进程何时死的无从得知，留空比填 now 诚实。
func markStale(a *Action) {
	a.Status = StatusStale
	if a.Error == nil {
		a.Error = &Error{
			Code:    types.CodeExec,
			Message: fmt.Sprintf("进程已消失（PID %d 不存在），任务被中断，结果未知", a.Pid),
			Detail:  "常见原因：被 OOM 杀、被 kill -9、机器重启或断电——中断前没有机会写结束状态；结束时间未知",
			Hint:    "重跑该命令即可；若是内存不足，缩小输入或放宽预算（--max-memory / --load-mode stream）",
		}
	}
}
