package action

import (
	"fmt"

	"github.com/dezhishen/dtool/internal/workspace"
	"github.com/dezhishen/dtool/pkg/types"
)

// Reconcile 把「进程已死的 running」在磁盘上收敛为 stale。
//
// 进程被强杀（OOM、kill -9、机器重启）时来不及写结束状态，Action 文件会永远停在
// running：读路径（actions list / show）会按 pid 显示成 stale，但直接读
// `.dtool/actions/<id>.json` 的脚本、AI 或 git diff 看到的仍是 running，会误以为任务
// 还在跑。这里把结论落盘——Action 文件与 index.json 一起改，只在确有陈旧条目时才写。
//
// 返回被收敛的条数。
func (r *Recorder) Reconcile() (int, error) {
	// 与 save/annotate 共用工作区锁：回收会改写 Action 文件与 index.json，
	// 不能与别的进程的写入交错（否则可能出现「后写覆盖前写」丢条目）。
	unlock, err := r.WS.Lock()
	if err != nil {
		return 0, err
	}
	defer unlock()
	idx := r.loadIndex()
	fixed := 0
	for i := range idx.Actions {
		e := idx.Actions[i]
		if e.Status != StatusRunning || pidAlive(e.Pid) {
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
			return fixed, err
		}
		idx.Actions[i] = entryOf(a)
		fixed++
	}
	if fixed == 0 {
		return 0, nil
	}
	return fixed, workspace.WriteJSONAtomic(r.WS.IndexPath(), idx)
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
