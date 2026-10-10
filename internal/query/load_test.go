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

// auto 选整块解析时要留余量：估算擦着预算通过，实际往往在中途被看门狗中止。
func TestResolveModeAutoKeepsHeadroom(t *testing.T) {
	size := uint64(1 << 20) // 整块解析预计 13MB
	cases := []struct {
		available uint64
		want      LoadMode
	}{
		{20 << 20, LoadFull},   // 13MB ≤ 16MB，留有余量
		{15 << 20, LoadStream}, // 13MB > 12MB，太紧 -> 流式
	}
	for _, c := range cases {
		mem := memguard.Memory{Available: c.available, Source: "test"}
		if got := resolveMode("auto", size, mem); got != c.want {
			t.Errorf("resolveMode(auto, %d, available=%d) = %s, want %s", size, c.available, got, c.want)
		}
	}
	// 显式 full 不受余量影响（用户说了算），但 fullTight 会提示
	if got := resolveMode("full", size, memguard.Memory{Available: 15 << 20}); got != LoadFull {
		t.Errorf("显式 full 应保持 full，得到 %s", got)
	}
	if !fullTight(size, memguard.Memory{Available: 15 << 20, Source: "test"}) {
		t.Error("15MB 预算下 13MB 的整块解析应被判为逼近预算")
	}
	if fullTight(size, memguard.Memory{Available: 20 << 20, Source: "test"}) {
		t.Error("20MB 预算有余量，不该提示")
	}
	if fullTight(size, memguard.Memory{}) {
		t.Error("预算未知时不该提示")
	}
}
