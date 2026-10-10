package action

import (
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/dezhishen/dtool/internal/workspace"
)

// ReapArg 是看护进程的隐藏子命令（在 cmd 包注册，不对外暴露）。
const ReapArg = "__reap"

// ReapPoll 是看护进程检查主进程是否还在的间隔。
const ReapPoll = 250 * time.Millisecond

// ReapMaxWait 是看护进程的最长存活时间，防止残留进程永久守着。
const ReapMaxWait = 24 * time.Hour

// PidAlive 供看护进程判断主进程是否还在（与 stale 判定同一个实现）。
func PidAlive(pid int) bool { return pidAlive(pid) }

var reaperOnce sync.Once

// StartReaper 起一个独立的看护进程，只做一件事：本进程**没有机会收尾**地消失时
// （SIGKILL / TerminateProcess / 被 OOM 杀 / 断电重启），把本进程遗留的 running
// 落盘为 stale。
//
// 为什么必须另起进程：进程内的补偿代码在强杀时根本来不及跑（这正是「强杀后 Action
// 停在 running」的根因），只有别的进程能替它收尾。看护进程只轮询 pid，主进程一消失
// 就收敛一次（幂等、带工作区锁），开销很小。
//
// 每个进程只起一次；DTOOL_NO_REAPER 非空可关闭（受限沙箱不允许起子进程时）。
// 返回是否真的起来了；失败不影响主流程（best-effort）。
func StartReaper(ws *workspace.Workspace) bool {
	if ws == nil || os.Getenv("DTOOL_NO_REAPER") != "" {
		return false
	}
	started := false
	reaperOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			return
		}
		cmd := exec.Command(exe, ReapArg, "--workspace", ws.Root, "--pid", strconv.Itoa(os.Getpid()))
		cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
		if err := cmd.Start(); err != nil {
			return
		}
		// 不 Wait：看护进程要活到主进程结束之后；Release 把回收交给内核。
		_ = cmd.Process.Release()
		started = true
	})
	return started
}

// ReapWatch 由看护进程调用：等 pid 消失后收敛一次 Action 状态，返回收敛条数。
// 主进程一直活着（超过 maxWait）时什么都不做。
func ReapWatch(ws *workspace.Workspace, pid int, interval, maxWait time.Duration) int {
	if ws == nil || pid <= 0 {
		return 0
	}
	if interval <= 0 {
		interval = ReapPoll
	}
	if maxWait <= 0 {
		maxWait = ReapMaxWait
	}
	for deadline := time.Now().Add(maxWait); ; {
		if !pidAlive(pid) {
			break
		}
		if time.Now().After(deadline) {
			return 0
		}
		time.Sleep(interval)
	}
	rep, err := (&Recorder{WS: ws}).Sync()
	if err != nil {
		return 0
	}
	return rep.Stale
}
