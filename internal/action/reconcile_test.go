package action

import (
	"os"
	"strings"
	"testing"

	"github.com/dezhishen/dtool/internal/workspace"
)

// 进程被强杀时来不及写结束状态：只有把结论落盘，读 `.dtool/actions/<id>.json` 的
// 人才不会以为任务还在跑。
func TestReconcilePersistsStale(t *testing.T) {
	r := newRec(t)

	dead := mustStart(t, r, "query", StartParams{Command: "dtool query"})
	dead.Pid = 2147480000 // 不存在的进程
	if err := r.save(dead); err != nil {
		t.Fatal(err)
	}
	live := mustStart(t, r, "convert", StartParams{}) // 本进程 pid，属于真正在跑的

	n, err := r.Reconcile()
	if err != nil || n != 1 {
		t.Fatalf("Reconcile = %d, %v", n, err)
	}

	var raw Action
	if err := workspace.ReadJSON(r.WS.ActionPath(dead.ID), &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Status != StatusStale {
		t.Fatalf("action 文件状态 = %s，应落盘为 stale", raw.Status)
	}
	if raw.Error == nil || !strings.Contains(raw.Error.Message, "进程已消失") || raw.Error.Hint == "" {
		t.Fatalf("stale 缺少可读原因：%+v", raw.Error)
	}
	if raw.FinishedAt != nil {
		t.Fatal("进程何时死的无从得知，不该臆造结束时间")
	}

	var idx Index
	if err := workspace.ReadJSON(r.WS.IndexPath(), &idx); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range idx.Actions {
		got[e.ID] = e.Status
	}
	if got[dead.ID] != StatusStale || got[live.ID] != StatusRunning {
		t.Fatalf("index 状态 = %+v", got)
	}

	// 幂等：收敛过之后不再写盘
	if n, err := r.Reconcile(); err != nil || n != 0 {
		t.Fatalf("第二次 Reconcile = %d, %v", n, err)
	}
}

// 权限损坏的目录不应让整个命令失败（Reconcile 由每个命令入口调用）。
func TestReconcileIgnoresBrokenFiles(t *testing.T) {
	r := newRec(t)
	a := mustStart(t, r, "query", StartParams{})
	a.Pid = 2147480000
	if err := r.save(a); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.WS.ActionPath(a.ID), []byte("{ 坏掉的 JSON"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n, err := r.Reconcile(); err != nil || n != 0 {
		t.Fatalf("Reconcile = %d, %v（应跳过损坏文件）", n, err)
	}
}

// Sync 除了收敛，还要报告「确实还在跑」的任务，否则调用方无法区分
// 「已收敛」与「真在运行」。
func TestSyncReportsState(t *testing.T) {
	r := newRec(t)
	dead := mustStart(t, r, "query", StartParams{})
	dead.Pid = 2147480000
	if err := r.save(dead); err != nil {
		t.Fatal(err)
	}
	live := mustStart(t, r, "convert", StartParams{})

	rep, err := r.Sync()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Scanned != 2 || rep.Stale != 1 || len(rep.StaleIDs) != 1 || rep.StaleIDs[0] != dead.ID {
		t.Fatalf("report = %+v", rep)
	}
	if len(rep.Running) != 1 || rep.Running[0].ID != live.ID {
		t.Fatalf("running = %+v", rep.Running)
	}
	// 再同步一次：没有陈旧条目，不应产生新的收敛
	rep2, err := r.Sync()
	if err != nil || rep2.Stale != 0 || len(rep2.Running) != 1 {
		t.Fatalf("second sync = %+v, %v", rep2, err)
	}
}
