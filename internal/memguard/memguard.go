// Package memguard 提供内存预算探测、加载前预估与运行期看门狗。
// 目的是把「被内核 OOM 静默杀掉」变成「带原因和处置建议的普通错误」。
package memguard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dezhishen/dtool/pkg/types"
)

const (
	// GuardMargin：估算预算时只用可用内存的 85%，留出安全余量。
	GuardMargin = 85
	// DefaultProgressMinSize：超过该大小的输入会打印载入进度，进程被强杀后也能看出卡在哪。
	DefaultProgressMinSize = 8 << 20
)

// ErrPressure 由看门狗触发，经 context.Cause 传回。
var ErrPressure = errors.New("memory pressure")

// WatchInterval 可在测试中调整。
var WatchInterval = 200 * time.Millisecond

// NoticeWriter 是看门狗触发时的提示输出；测试可替换。进程正在做不可中断的工作
// （例如整块解析大 JSON）时，取消要等它跑完才会被观察到，提示先让用户知道发生了什么。
var NoticeWriter io.Writer = os.Stderr

// Memory 描述本次运行的内存预算。
type Memory struct {
	Limit     uint64 // 上限（cgroup 或显式配置），0 表示未知
	Used      uint64 // 已用（cgroup 口径），未知为 0
	Available uint64 // 预计可用，0 表示不做检查
	Source    string // 判定来源，用于报错信息
}

// ParseBytes 解析 512M / 1.5G / 2097152 这类容量；0 或空表示不限制。
func ParseBytes(s string) (uint64, error) {
	t := strings.TrimSpace(strings.ToUpper(s))
	if t == "" || t == "0" {
		return 0, nil
	}
	mult := uint64(1)
	switch t[len(t)-1] {
	case 'K':
		mult, t = 1<<10, t[:len(t)-1]
	case 'M':
		mult, t = 1<<20, t[:len(t)-1]
	case 'G':
		mult, t = 1<<30, t[:len(t)-1]
	case 'T':
		mult, t = 1<<40, t[:len(t)-1]
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
	if err != nil || f <= 0 {
		return 0, types.Errorf(types.CodeUsage, "invalid memory size %q", s).
			WithHint("例如 512M / 1.5G；0 表示不限制")
	}
	return uint64(f * float64(mult)), nil
}

// HumanSize 把字节数格式化成便于阅读的 1.5GB / 200MB / 8KB。
func HumanSize(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0fMB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0fKB", float64(n)/float64(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}

// Budget 决定内存预算：显式 --max-memory 优先，其次 cgroup / 系统可用内存；都拿不到时 Available 为 0（不检查）。
// budgetFrom 按「外部硬上限」推导可用预算：min(上限-已用, 系统可用)。
// 上限未知（0）时退回系统可用内存；两者都没有则返回 0（不检查）。
// 抽出来是为了让 Windows Job Object 的判定能在 Linux 上单测。
func budgetFrom(limit, used, sysAvail uint64) uint64 {
	var avail uint64
	if limit > 0 {
		if limit <= used {
			return 0 // 已经顶到上限：不再拿系统可用内存当退路
		}
		avail = limit - used
	}
	if sysAvail > 0 && (avail == 0 || sysAvail < avail) {
		avail = sysAvail // 上限未知，或系统可用更紧：取更紧的那个
	}
	return avail
}

func Budget(explicit *uint64) Memory {
	if explicit != nil {
		if *explicit == 0 {
			return Memory{Source: "--max-memory=0（已关闭内存检查）"}
		}
		return Memory{Limit: *explicit, Available: *explicit * GuardMargin / 100, Source: "--max-memory"}
	}
	m := Detect()
	m.Available = m.Available * GuardMargin / 100
	return m
}

// 处置建议要按场景给：--load-mode 只对 query 生效，
// 把它发给 convert 会让人以为换个参数就能过。
const (
	// HintLoadMode 用于 query 的 JSON 装入。
	HintLoadMode = "改用流式解析（--load-mode stream）、拆分或裁剪输入后重试；确需强制运行时加 --max-memory 0 关闭该检查"
	// HintSplitInput 用于 convert 等本来就走流式、没有开关可切的路径：
	// 峰值只随文件体积增长，与行数无关，所以建议只能落在「缩小输入」上。
	HintSplitInput = "拆分或裁剪输入（裁列、去掉多余工作表，或改用多个小文件）后重试；确需强制运行时加 --max-memory 0 关闭该检查"
)

// CheckSize 按单一倍率估算峰值内存，见 CheckNeed。
func CheckSize(label string, size uint64, factor int, mem Memory, hint string) error {
	return CheckNeed(label, size, size*uint64(factor), fmt.Sprintf("按实测 %d 倍估算", factor), mem, hint)
}

// CheckNeed 在载入前判断预估峰值 need 是否放得下；how 用于说明估算依据，
// hint 是给该场景的处置建议（见 HintLoadMode / HintSplitInput）。
// mem.Available 为 0 时不做检查。
func CheckNeed(label string, size, need uint64, how string, mem Memory, hint string) error {
	if mem.Available == 0 || size == 0 {
		return nil
	}
	if need <= mem.Available {
		return nil
	}
	return types.Errorf(types.CodeExec, "预计内存不足，已中止：%s %s 预计需约 %s", label, HumanSize(size), HumanSize(need)).
		WithDetail(fmt.Sprintf("%s需 %s，当前可用约 %s（%s）", how, HumanSize(need), HumanSize(mem.Available), mem.Source)).
		WithHint(hint)
}

// Watch 监控本进程内存用量，超过 limit 即取消 ctx，使加载/查询以普通错误退出。
// limit 为 0 时不监控。返回的 stop 必须调用。
func Watch(ctx context.Context, limit uint64) (context.Context, func()) {
	if limit == 0 {
		return ctx, func() {}
	}
	cctx, cancel := context.WithCancelCause(ctx)
	var once sync.Once
	done := make(chan struct{})
	stop := func() { once.Do(func() { close(done) }) }
	go func() {
		t := time.NewTicker(WatchInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-cctx.Done():
				return
			case <-t.C:
				if u := CurrentUsage(); u > limit {
					fmt.Fprintf(NoticeWriter,
						"内存超出预算（本进程已用 %s，预算 %s），正在中止；\n"+
							"若迟迟没有输出，请 Ctrl+C 后用 --load-mode stream 或调大 --max-memory 重试。\n",
						HumanSize(u), HumanSize(limit))
					cancel(fmt.Errorf("%w: 本进程已用 %s，本次预算 %s", ErrPressure, HumanSize(u), HumanSize(limit)))
					return
				}
			}
		}
	}()
	return cctx, stop
}

// Progress 对较大的输入打印载入提示，便于进程被强杀后定位。
func Progress(w io.Writer, minSize uint64, label string, size, need uint64, note string) {
	if size < minSize {
		return
	}
	fmt.Fprintf(w, "载入 %s（%s，%s，预计需约 %s 内存）...\n", label, HumanSize(size), note, HumanSize(need))
}

// PressureError 把 context 中的内存压力原因包装成用户可读错误；无压力时返回 nil。
func PressureError(ctx context.Context) error {
	cause := context.Cause(ctx)
	if !errors.Is(cause, ErrPressure) {
		return nil
	}
	return types.Errorf(types.CodeExec, "内存即将耗尽，已中止：%v", cause).
		WithHint("拆分或裁剪输入、去掉不必要的列；确认内存充足时用 --max-memory 0 关闭检查")
}

// SizeOf 返回文件大小；出错或非普通文件返回 0。
func SizeOf(path string) uint64 {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return 0
	}
	return uint64(st.Size())
}
