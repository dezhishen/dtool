package action

import (
	"strings"
	"testing"
	"time"

	"github.com/dezhishen/dtool/internal/workspace"
)

// 看护进程的核心动作：主进程消失后收敛一次。
func TestReapWatchConvergesAfterExit(t *testing.T) {
	r := newRec(t)
	a := mustStart(t, r, "query", StartParams{})
	a.Pid = 2147480000 // 已不存在的进程
	if err := r.save(a); err != nil {
		t.Fatal(err)
	}

	if n := ReapWatch(r.WS, a.Pid, time.Millisecond, time.Second); n != 1 {
		t.Fatalf("ReapWatch 收敛 %d 条，期望 1", n)
	}
	var raw Action
	if err := workspace.ReadJSON(r.WS.ActionPath(a.ID), &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Status != StatusStale || raw.Error == nil || !strings.Contains(raw.Error.Message, "进程已消失") {
		t.Fatalf("收敛结果不对：%+v", raw)
	}
}

// 主进程还活着时什么都不做（别把正在跑的任务标成 stale）。
func TestReapWatchWaitsWhileAlive(t *testing.T) {
	r := newRec(t)
	a := mustStart(t, r, "query", StartParams{}) // pid = 本测试进程，活着
	start := time.Now()
	if n := ReapWatch(r.WS, a.Pid, time.Millisecond, 30*time.Millisecond); n != 0 {
		t.Fatalf("活着的主进程不该被收敛，得到 %d", n)
	}
	if elapsed := time.Since(start); elapsed < 25*time.Millisecond {
		t.Fatalf("应当一直等到 maxWait 才放弃，实际 %s", elapsed)
	}
	got, _ := r.Get(a.ID)
	if got.Status != StatusRunning {
		t.Fatalf("状态被改动：%s", got.Status)
	}
}

// 参数非法 / 空工作区不该 panic（看护进程永不报错）。
func TestReapWatchGuards(t *testing.T) {
	r := newRec(t)
	if n := ReapWatch(nil, 123, time.Millisecond, time.Millisecond); n != 0 {
		t.Fatal("nil 工作区应返回 0")
	}
	if n := ReapWatch(r.WS, 0, time.Millisecond, time.Millisecond); n != 0 {
		t.Fatal("pid<=0 应返回 0")
	}
}
