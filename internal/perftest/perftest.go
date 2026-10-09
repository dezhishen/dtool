// Package perftest 提供性能测试用的公共小工具。
// 它只被 *_test.go 引用，不会进入正式二进制（因此可以为测试保留这份便利）。
package perftest

import (
	"runtime"
	"sync/atomic"
	"time"
)

// InclusiveInterval / LiveInterval 是两种采样方式的时间间隔。
// Live 每次采样都要停一次世界，间隔必须比 Inclusive 大，否则会把时间全花在 GC 上。
var (
	InclusiveInterval = 2 * time.Millisecond
	LiveInterval      = 10 * time.Millisecond
)

// Mode 决定峰值怎么采。
type Mode int

const (
	// Inclusive 直接读 HeapAlloc：包含尚未回收的垃圾，数值更接近进程 RSS，
	// 适合「不得超过某个预算」这类上界断言。缺点是受 GC 时机影响，抖动大。
	Inclusive Mode = iota
	// Live 每次采样前先 GC，读到的是存活堆：不受 GC 时机影响，反映「同时驻留
	// 多少内存」，适合比较两种实现谁更省内存。
	Live
)

// Stats 是一次测量得到的两个互补指标：
//
//	Peak  — 按 Mode 采样的峰值；
//	Alloc — 累计分配字节数（TotalAlloc 增量），只增不减，可用来判断是否产生了
//	        额外的分配压力（注意它看不出驻留量）。
type Stats struct {
	Peak  uint64
	Alloc uint64
}

// Measure 执行 fn 并采集 Stats。
func Measure(fn func(), mode Mode) Stats {
	interval := InclusiveInterval
	if mode == Live {
		interval = LiveInterval
	}
	var peak atomic.Uint64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var ms runtime.MemStats
		tk := time.NewTicker(interval)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
			}
			if mode == Live {
				runtime.GC() // 让 HeapAlloc 反映存活集，而不是尚未回收的垃圾
			}
			runtime.ReadMemStats(&ms)
			if h := ms.HeapAlloc; h > peak.Load() {
				peak.Store(h)
			}
		}
	}()

	var before, after runtime.MemStats
	runtime.GC() // 先回收上一轮的残留，避免算进本次
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	close(stop)
	<-done

	s := Stats{Peak: peak.Load()}
	if after.TotalAlloc > before.TotalAlloc {
		s.Alloc = after.TotalAlloc - before.TotalAlloc
	}
	return s
}

// MiB 把字节数换算成 MiB，便于日志阅读。
func MiB(n uint64) float64 { return float64(n) / (1 << 20) }
