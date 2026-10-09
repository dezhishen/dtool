package memguard

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dezhishen/dtool/pkg/types"
)

func TestParseBytes(t *testing.T) {
	ok := map[string]uint64{
		"": 0, "0": 0, "512": 512, "512K": 512 << 10, "512k": 512 << 10,
		"1.5G": 3 << 29, "2G": 2 << 30, " 1T ": 1 << 40,
	}
	for in, want := range ok {
		if got, err := ParseBytes(in); err != nil || got != want {
			t.Errorf("ParseBytes(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"abc", "-1", "1Gx", "G"} {
		_, err := ParseBytes(in)
		var te *types.Error
		if !errors.As(err, &te) || te.Code != types.CodeUsage || te.Hint == "" {
			t.Errorf("ParseBytes(%q) err = %v", in, err)
		}
	}
}

func TestHumanSize(t *testing.T) {
	cases := map[uint64]string{512: "512B", 8 << 10: "8KB", 100 << 20: "100MB", 3 << 29: "1.5GB"}
	for in, want := range cases {
		if got := HumanSize(in); got != want {
			t.Errorf("HumanSize(%d) = %s, want %s", in, got, want)
		}
	}
}

func TestBudget(t *testing.T) {
	zero := uint64(0)
	if m := Budget(&zero); m.Available != 0 || !strings.Contains(m.Source, "关闭") {
		t.Fatalf("zero budget must disable checks: %+v", m)
	}
	one := uint64(2 << 30)
	m := Budget(&one)
	if m.Limit != 2<<30 || m.Available != (2<<30)*GuardMargin/100 || m.Source != "--max-memory" {
		t.Fatalf("explicit budget: %+v", m)
	}
	if auto := Budget(nil); auto.Source == "" {
		t.Fatalf("auto budget should carry a source: %+v", auto)
	}
}

func TestCheckSize(t *testing.T) {
	const factor = 13
	if err := CheckSize("数据源", 1<<20, factor, Memory{}, HintLoadMode); err != nil {
		t.Fatalf("unknown budget must not block: %v", err)
	}
	if err := CheckSize("数据源", 0, factor, Memory{Available: 1}, HintLoadMode); err != nil {
		t.Fatalf("empty input must not block: %v", err)
	}
	if err := CheckSize("数据源", 1<<20, factor, Memory{Available: 100 << 20, Source: "t"}, HintLoadMode); err != nil {
		t.Fatalf("enough memory: %v", err)
	}
	err := CheckSize("数据源", 1<<20, factor, Memory{Available: 1 << 20, Source: "test"}, HintLoadMode)
	var te *types.Error
	if !errors.As(err, &te) || te.Code != types.CodeExec {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"内存不足", "1MB", "13MB"} {
		if !strings.Contains(te.Message+te.Detail, want) {
			t.Errorf("missing %q in %q / %q", want, te.Message, te.Detail)
		}
	}
	if !strings.Contains(te.Detail, "13 倍") || !strings.Contains(te.Hint, "--max-memory 0") ||
		!strings.Contains(te.Hint, "--load-mode stream") {
		t.Fatalf("not actionable: %q / %q", te.Detail, te.Hint)
	}
	// CheckNeed 允许按模式传入不同的估算依据
	if err := CheckNeed("数据源", 1<<20, 3<<20, "流式装入按 2 倍估算", Memory{Available: 4 << 20, Source: "t"}, HintLoadMode); err != nil {
		t.Fatalf("CheckNeed should pass: %v", err)
	}
	// convert 场景没有 --load-mode 开关，提示不能张冠李戴
	err = CheckSize("Excel 文件", 1<<20, 260, Memory{Available: 1 << 20, Source: "test"}, HintSplitInput)
	if !errors.As(err, &te) || strings.Contains(te.Hint, "--load-mode") || !strings.Contains(te.Hint, "拆分") {
		t.Fatalf("convert hint = %v", err)
	}

	err = CheckNeed("数据源", 1<<20, 3<<20, "流式装入按 2 倍估算", Memory{Available: 2 << 20, Source: "t"}, HintLoadMode)
	if !errors.As(err, &te) || !strings.Contains(te.Detail, "流式装入按 2 倍估算") {
		t.Fatalf("CheckNeed err = %v", err)
	}
	if !strings.Contains(te.Hint, "--load-mode stream") {
		t.Fatalf("query hint = %q", te.Hint)
	}
}

func TestWatchCancelsOnPressure(t *testing.T) {
	old := WatchInterval
	WatchInterval = 10 * time.Millisecond
	defer func() { WatchInterval = old }()

	ctx, stop := Watch(context.Background(), 1) // 1 字节预算，必然超
	defer stop()
	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("watchdog did not fire")
	}
	if !errors.Is(context.Cause(ctx), ErrPressure) {
		t.Fatalf("cause = %v", context.Cause(ctx))
	}
	pe := PressureError(ctx)
	var pte *types.Error
	if !errors.As(pe, &pte) || !strings.Contains(pte.Message, "内存即将耗尽") || pte.Hint == "" {
		t.Fatalf("PressureError = %v", pe)
	}

	ctx2, stop2 := Watch(context.Background(), 0) // 不监控
	stop2()
	select {
	case <-ctx2.Done():
		t.Fatal("limit 0 must not cancel")
	case <-time.After(50 * time.Millisecond):
	}
	if PressureError(context.Background()) != nil {
		t.Fatal("no pressure must map to nil")
	}
}

func TestProgress(t *testing.T) {
	var buf bytes.Buffer
	Progress(&buf, 1<<20, "small.json", 1<<10, 20<<10, "流式解析")
	if buf.Len() != 0 {
		t.Fatalf("below threshold must stay quiet: %q", buf.String())
	}
	Progress(&buf, 1<<20, "big.json", 2<<20, 26<<20, "整块解析")
	got := buf.String()
	if !strings.Contains(got, "载入 big.json") || !strings.Contains(got, "2MB") ||
		!strings.Contains(got, "26MB") || !strings.Contains(got, "整块解析") {
		t.Fatalf("progress = %q", got)
	}
}

func TestSizeOf(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.json")
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), 1234), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := SizeOf(p); got != 1234 {
		t.Fatalf("SizeOf = %d", got)
	}
	if got := SizeOf(dir); got != 0 {
		t.Fatalf("directory should be 0, got %d", got)
	}
	if got := SizeOf(filepath.Join(dir, "missing")); got != 0 {
		t.Fatalf("missing should be 0, got %d", got)
	}
}

func TestDetectAndUsage(t *testing.T) {
	if m := Detect(); m.Source == "" {
		t.Fatalf("source should always be filled: %+v", m)
	}
	if u := CurrentUsage(); u == 0 {
		t.Fatal("CurrentUsage returned 0")
	}
	// GOMEMLIMIT 之类的显式环境变量必须被识别
	t.Setenv("DTOOL_MAX_MEMORY", "3G")
	if m := Detect(); m.Limit != 3<<30 || !strings.Contains(m.Source, "DTOOL_MAX_MEMORY") {
		t.Fatalf("env override: %+v", m)
	}
	t.Setenv("DTOOL_MAX_MEMORY", "bad")
	if m := Detect(); strings.Contains(m.Source, "DTOOL_MAX_MEMORY") {
		t.Fatalf("bad env should be ignored: %+v", m)
	}
}
