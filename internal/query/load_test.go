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
