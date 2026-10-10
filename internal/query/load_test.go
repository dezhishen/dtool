package query

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dezhishen/dtool/internal/memguard"
	"github.com/dezhishen/dtool/pkg/types"
)

func writeRowsJSON(t *testing.T, n int) (string, []byte) {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("[")
	for i := 0; i < n; i++ {
		if i > 0 {
			buf.WriteByte(',')
		}
		fmt.Fprintf(&buf, `{"a":%d,"b":"x%d"}`, i, i)
	}
	buf.WriteString("]")
	p := filepath.Join(t.TempDir(), "d.json")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p, buf.Bytes()
}

// 整块解析改成逐元素解码后，结果必须与原实现（json.Unmarshal）完全一致。
func TestDecodeRowsEquivalentToUnmarshal(t *testing.T) {
	p, raw := writeRowsJSON(t, 5000)
	var want []types.Row
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := decodeRows(context.Background(), bufio.NewReaderSize(f, 1<<20), "d.json")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decodeRows 结果与 json.Unmarshal 不一致：got %d 行，want %d 行", len(got), len(want))
	}
}

// 已取消的 ctx 必须立刻返回：否则内存看门狗触发后还要等整个 Unmarshal 跑完，
// 大文件下就是长时间静默卡住。
func TestDecodeRowsHonorsCanceledContext(t *testing.T) {
	p, _ := writeRowsJSON(t, 5000)
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := decodeRows(ctx, bufio.NewReader(f), "d.json"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v，应为 context.Canceled", err)
	}
}

func TestDecodeRowsRejectsNonArray(t *testing.T) {
	for _, in := range []string{`{"a":1}`, `[1,2`, `not json`} {
		if _, err := decodeRows(context.Background(), strings.NewReader(in), "d.json"); err == nil {
			t.Fatalf("输入 %q 应报错", in)
		} else if !strings.Contains(err.Error(), "not a JSON array of objects") {
			t.Fatalf("输入 %q 的错误不可读：%v", in, err)
		}
	}
}

// 选档：预算宽松时用最快的档，紧了就降档；操作者强制指定优先于预算。
func TestChooseLadderByBudget(t *testing.T) {
	size := uint64(10 << 20)
	cases := []struct {
		name      string
		available uint64
		hard      bool
		want      string
	}{
		// 10MB 输入：full 预计 32+130=162MB、stream+memory 52MB、stream+disk 27MB
		{"预算宽松", 2 << 30, true, "full+memory"},       // 162MB ≤ 1.6GB 阈值
		{"整块放不下", 100 << 20, false, "stream+memory"}, // 162MB > 100MB，流式 52MB 放得下
		{"只剩磁盘档", 40 << 20, true, "stream+disk"},     // 硬上限阈值 32MB：52MB 放不下，27MB 放得下
	}
	for _, c := range cases {
		plan, spec, _, err := choosePlan(Options{}, memguard.Memory{Available: c.available, Hard: c.hard, Source: "test"}, size)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if spec.Name != c.want {
			t.Errorf("%s: 选中 %s, want %s（plan=%+v）", c.name, spec.Name, c.want, plan)
		}
	}
}

// 操作者强行指定：full 与 disk 的组合不存在（full 的峰值在 Go 堆，换库不省），
// 要给出可读的用法错误；单独指定则只剩一档、等于强制。
func TestForceStrategy(t *testing.T) {
	if _, _, err := candidates("full", "disk"); err == nil {
		t.Fatal("full+disk 不是有效档，应报用法错误")
	}
	cands, forced, err := candidates("full", "")
	if err != nil || forced != "full+memory" || len(cands) != 1 {
		t.Fatalf("--load-mode full 应只剩 full+memory：%v %v %v", cands, forced, err)
	}
	cands, forced, err = candidates("", "disk")
	if err != nil || forced != "stream+disk" || len(cands) != 1 {
		t.Fatalf("--store disk 应只剩 stream+disk：%v %v %v", cands, forced, err)
	}
	cands, forced, err = candidates("auto", "auto")
	if err != nil || forced != "" || len(cands) != len(ladder) {
		t.Fatalf("都不指定应给完整阶梯：%v %v %v", cands, forced, err)
	}
}

// --mem-policy strict：连最省档都放不下时直接失败，不试。
func TestMemPolicyStrictRefuses(t *testing.T) {
	tiny := uint64(1 << 20)
	_, _, _, err := choosePlan(Options{MemPolicy: "strict"},
		memguard.Memory{Available: tiny, Hard: true, Source: "test"}, 512<<20)
	var te *types.Error
	if !errors.As(err, &te) || te.Code != types.CodeExec {
		t.Fatalf("strict 应直接失败：%v", err)
	}
	for _, want := range []string{"最省档", "阈值"} {
		if !strings.Contains(te.Detail, want) {
			t.Fatalf("detail 缺少 %q：%q", want, te.Detail)
		}
	}
	if !strings.Contains(te.Hint, "--max-memory 0") {
		t.Fatalf("hint 应给出退出口：%q", te.Hint)
	}
}

// 上限读不到时（Windows Job 标志位设了、值却是 0）：预算只是「本机空闲内存」，
// 不能拿它去选峰值比输入大一个数量级的整块解析档——沙箱真限 256MB 而输入 118MB 时，
// 那一下就是不可恢复的 OOM。操作者显式指定时不干预（他就是要试）。
func TestUncertainCapDropsFullRung(t *testing.T) {
	size := uint64(118 << 20)
	unc := memguard.Memory{Available: 14 << 30, Uncertain: true, Source: "系统可用内存（Job Object 上限读不到，预算不可信）"}

	plan, spec, _, err := choosePlan(Options{}, unc, size)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Name != "stream+memory" {
		t.Fatalf("预算不可信时不该选整块解析：%s（%s）", spec.Name, plan.Reason)
	}
	if !strings.Contains(plan.Reason, "整块解析") {
		t.Fatalf("理由要写明为什么排除了整块解析：%q", plan.Reason)
	}

	// 操作者强制 full 时照办
	_, spec, _, err = choosePlan(Options{LoadMode: "full"}, unc, size)
	if err != nil || spec.Mode != LoadFull {
		t.Fatalf("显式 --load-mode full 应被尊重：%+v %v", spec, err)
	}

	// 上限可信时（硬上限 256MB）仍按阶梯正常判断
	known := memguard.Memory{Available: 205 << 20, Hard: true, Source: "Windows Job Object 进程内存上限"}
	_, spec, _, err = choosePlan(Options{}, known, size)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Name != "stream+disk" {
		t.Fatalf("256MB 硬上限 + 118MB 输入应落到磁盘档：%s", spec.Name)
	}
}
