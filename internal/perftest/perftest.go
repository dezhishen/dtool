// Package perftest 提供性能测试用的公共小工具。
// 它只被 *_test.go 引用，不会进入正式二进制（因此可以为测试保留这份便利）。
package perftest

import (
	"runtime"
	"sync/atomic"
	"time"
)

// SampleInterval 是峰值采样的间隔；测试里可调整。
var SampleInterval = 2 * time.Millisecond

// Stats 是一次测量得到的两个互补指标：
//
//	Peak  — 执行期间的峰值堆占用（HeapAlloc），反映「同时驻留多少内存」；
//	Alloc — 累计分配字节数（TotalAlloc 增量），反映「总共产生多少垃圾」。
//
// Peak 会因 GC 时机抖动，Alloc 是单调计数器、几乎不受 GC 影响但看不出驻留量，
// 所以内存量级的断言两者都看，单项噪声就不会造成假失败。
type Stats struct {
	Peak  uint64
	Alloc uint64
}

// Measure 执行 fn 并在同一轮里采集 Peak 与 Alloc。
func Measure(fn func()) Stats {
	var peak atomic.Uint64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var ms runtime.MemStats
		tk := time.NewTicker(SampleInterval)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				runtime.ReadMemStats(&ms)
				if h := ms.HeapAlloc; h > peak.Load() {
					peak.Store(h)
				}
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

// PeakHeap 只需要峰值时用它的简写。
func PeakHeap(fn func()) uint64 { return Measure(fn).Peak }

// MiB 把字节数换算成 MiB，便于日志阅读。
func MiB(n uint64) float64 { return float64(n) / (1 << 20) }
