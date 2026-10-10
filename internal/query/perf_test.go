package query

import (
	"bufio"
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dezhishen/dtool/internal/memguard"
	"github.com/dezhishen/dtool/internal/perftest"
)

// 本文件把 README「装入方式与大数据量」里的性能结论固化成可回归的测试：
//
//	TestPerfLoadModeMemoryRatio  流式的峰值堆必须显著低于整块解析（守住 8.4.2 的结论）
//	BenchmarkLoad*               装入吞吐，需显式开启（go test -bench）
//
// 运行方式：
//
//	go test ./internal/query -run Perf -v          # 内存倍率回归（默认用例即包含）
//	go test ./internal/query -bench . -benchmem    # 基准，默认 2 万行
//	DTOOL_BENCH_ROWS=200000 go test ./internal/query -bench . -benchmem   # 放大到 20 万行
//
// 基准不写死内存倍率，只报告吞吐（-benchmem 给出 allocs/op），
// 绝对内存数字随 Go 版本与 GC 行为漂移，写进断言只会变成噪音。

// perfRatioRows 是内存倍率回归的输入规模：约 4–5MB，够大以便信噪比，
// 又能在 1 秒量级跑完（默认 go test ./... 也会执行）。
const perfRatioRows = 60000

// benchRows 可用 DTOOL_BENCH_ROWS 覆盖，便于本地放大到生产规模。
func benchRows() int {
	if s := os.Getenv("DTOOL_BENCH_ROWS"); s != "" {
		var n int
		if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return 20000
}

// writeJSONRows 生成 rows 行混合类型数据（数值/字符串/布尔），返回路径与文件大小。
// 固定随机种子，结果可复现。
func writeJSONRows(tb testing.TB, dir string, rows int) (string, uint64) {
	tb.Helper()
	path := filepath.Join(dir, "perf.json")
	f, err := os.Create(path)
	if err != nil {
		tb.Fatal(err)
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	regions := [...]string{"华东", "华北", "华南", "西南", "东北"}
	rnd := rand.New(rand.NewPCG(1, 2))
	if _, err := w.WriteString("["); err != nil {
		tb.Fatal(err)
	}
	for i := 0; i < rows; i++ {
		if i > 0 {
			w.WriteString(",")
		}
		fmt.Fprintf(w, `{"id":%d,"region":%q,"amount":%.2f,"ok":%t,"memo":"row-%d"}`,
			i, regions[i%len(regions)], rnd.Float64()*1000, i%3 != 0, i)
	}
	if _, err := w.WriteString("]"); err != nil {
		tb.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		tb.Fatal(err)
	}
	st, err := f.Stat()
	if err != nil {
		tb.Fatal(err)
	}
	return path, uint64(st.Size())
}

// runLoad 在指定装入方式下跑一次「载入 + COUNT(*)」。
// MaxMemory 显式置 0：一是关掉预估，二是避免 debug.SetMemoryLimit 污染本进程后续测量。
func runLoad(tb testing.TB, path string, mode LoadMode, sql string) {
	tb.Helper()
	off := uint64(0)
	_, err := Run(context.Background(), Options{
		SQL:       sql,
		Sources:   map[string]string{"d": path},
		Sandbox:   true,
		MaxRows:   100, // 够 COUNT(*) 与 GROUP BY 用；--max-rows 触发会掩盖吞吐差异
		Timeout:   5 * time.Minute,
		MaxMemory: &off,
		LoadMode:  string(mode),
	})
	if err != nil {
		tb.Fatalf("load %s: %v", mode, err)
	}
}

// TestPerfLoadModeMemoryRatio 守住「流式装入的峰值内存显著低于整块解析」这一结论
// （README / Design 8.4.2），并顺带确认用例本身还有区分度。
// 主断言用相对比值而非绝对数字，避免不同机器、Go 版本与 GC 策略造成抖动。
func TestPerfLoadModeMemoryRatio(t *testing.T) {
	if testing.Short() {
		t.Skip("-short：跳过性能回归")
	}
	path, size := writeJSONRows(t, t.TempDir(), perfRatioRows)
	const count = `SELECT COUNT(*) AS n FROM d`

	full := perftest.Measure(func() { runLoad(t, path, LoadFull, count) }, perftest.Live)
	stream := perftest.Measure(func() { runLoad(t, path, LoadStream, count) }, perftest.Live)
	t.Logf("输入 %s（%d 行，%.1fMB）：full 峰值 %.1fMB / 累计分配 %.1fMB；stream 峰值 %.1fMB / 累计分配 %.1fMB；按文件大小 full ≈ ×%.1f、stream ≈ ×%.1f",
		filepath.Base(path), perfRatioRows, perftest.MiB(size),
		perftest.MiB(full.Peak), perftest.MiB(full.Alloc),
		perftest.MiB(stream.Peak), perftest.MiB(stream.Alloc),
		float64(full.Peak)/float64(size), float64(stream.Peak)/float64(size))

	// Alloc 只记录不断言：累计分配被 SQLite 插入开销主导（两种方式都在 120MB 左右，
	// 差距仅 3%），对「省内存」没有区分度，断言它只会变成假失败的来源。
	// 阈值取 1.3 倍：实测差距在 2.3–3.7 倍，留出余量吸收 GC 时机抖动，
	// 同时「流式被改回整块解析」这种量级退化一定会被拦住。
	if float64(full.Peak) < float64(stream.Peak)*1.3 {
		t.Errorf("流式装入应明显省内存：stream 峰值 %.1fMB vs full %.1fMB",
			perftest.MiB(stream.Peak), perftest.MiB(full.Peak))
	}
	// 整块解析的峰值应明显高于文件本身（README：≈ ×13），否则说明该用例已经没有区分度
	if full.Peak < size*4 {
		t.Errorf("full 峰值堆 %.1fMB 相对文件 %.1fMB 偏低，用例可能失去区分度",
			perftest.MiB(full.Peak), perftest.MiB(size))
	}
}

// benchRun 是三个基准的公共骨架：生成一次输入，重复「载入 + 查询」。
func benchRun(b *testing.B, mode LoadMode, sql string) {
	path, size := writeJSONRows(b, b.TempDir(), benchRows())
	b.SetBytes(int64(size))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runLoad(b, path, mode, sql)
	}
	rows := float64(benchRows() * b.N)
	b.ReportMetric(rows/b.Elapsed().Seconds(), "rows/s")
}

func BenchmarkLoadFullCount(b *testing.B) {
	benchRun(b, LoadFull, `SELECT COUNT(*) AS n FROM d`)
}

func BenchmarkLoadStreamCount(b *testing.B) {
	benchRun(b, LoadStream, `SELECT COUNT(*) AS n FROM d`)
}

// BenchmarkLoadStreamGroupBy 更接近真实用法：所有行都要进 SQLite 并参与聚合。
func BenchmarkLoadStreamGroupBy(b *testing.B) {
	benchRun(b, LoadStream, `SELECT region, COUNT(*) AS c, SUM(amount) AS s FROM d GROUP BY region ORDER BY c DESC`)
}

// BenchmarkLoadFullGroupBy 同上，整块解析，便于与流式横向对比。
func BenchmarkLoadFullGroupBy(b *testing.B) {
	benchRun(b, LoadFull, `SELECT region, COUNT(*) AS c, SUM(amount) AS s FROM d GROUP BY region ORDER BY c DESC`)
}

// BenchmarkChoosePlan 只是算术与查表，用于确认选档本身不是瓶颈。
func BenchmarkChoosePlan(b *testing.B) {
	mem := memguard.Memory{Available: 1 << 30, Source: "bench"}
	for i := 0; i < b.N; i++ {
		_, _, _, _ = choosePlan(Options{}, mem, 64<<20)
	}
}
