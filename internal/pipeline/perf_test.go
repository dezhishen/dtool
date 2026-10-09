package pipeline

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/dezhishen/dtool/internal/perftest"
	"github.com/dezhishen/dtool/internal/workspace"
	"github.com/xuri/excelize/v2"
)

// 本文件把 Excel 转换的性能特征固化成测试：吞吐走基准，内存倍率走回归用例
// （xlsxPeakFactor 就是预检所用的倍率，见 TestPerfConvertExcelMemory）。
//
//	go test ./internal/pipeline -run Perf -v        # 内存倍率回归
//	go test ./internal/pipeline -bench Convert -benchmem
//	DTOOL_BENCH_ROWS=150000 go test ./internal/pipeline -bench Convert   # 放大

// perfXlsxRows 是内存回归与基准的默认行数。1 万行（约 0.35MB）已足够拉开信噪比，
// 同时让默认的 go test ./... 只多花不到 1 秒；放大用 DTOOL_BENCH_ROWS。
func perfXlsxRows() int {
	if s := os.Getenv("DTOOL_BENCH_ROWS"); s != "" {
		var n int
		if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return 10000
}

// writeBigXlsx 生成 rows 行 × 5 列的 xlsx（固定随机种子，结果可复现），
// 返回路径与文件大小。用 StreamWriter 写入，生成开销与行数成线性。
func writeBigXlsx(tb testing.TB, dir string, rows int) (string, uint64) {
	tb.Helper()
	f := excelize.NewFile()
	sw, err := f.NewStreamWriter("Sheet1")
	if err != nil {
		tb.Fatal(err)
	}
	if err := sw.SetRow("A1", []any{"id", "region", "amount", "ok", "memo"}); err != nil {
		tb.Fatal(err)
	}
	regions := [...]string{"华东", "华北", "华南", "西南", "东北"}
	rnd := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < rows; i++ {
		cell, err := excelize.CoordinatesToCellName(1, i+2)
		if err != nil {
			tb.Fatal(err)
		}
		row := []any{i, regions[i%len(regions)], rnd.Float64() * 1000, i%3 != 0, fmt.Sprintf("row-%d", i)}
		if err := sw.SetRow(cell, row); err != nil {
			tb.Fatal(err)
		}
	}
	if err := sw.Flush(); err != nil {
		tb.Fatal(err)
	}
	path := filepath.Join(dir, "perf.xlsx")
	if err := f.SaveAs(path); err != nil {
		tb.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		tb.Fatal(err)
	}
	return path, uint64(st.Size())
}

// newPerfEnv 关闭内存预检（Budget 返回 0 → 不设全局 debug.SetMemoryLimit），
// 让测量反映纯转换开销；同时不记录 Action，避免磁盘写入干扰。
func newPerfEnv(tb testing.TB, dir string) *Env {
	tb.Helper()
	ws, err := workspace.Open(filepath.Join(dir, ".dtool"))
	if err != nil {
		tb.Fatal(err)
	}
	off := uint64(0)
	return &Env{Ctx: tb.Context(), WS: ws, Preview: 0, NoRecord: true, Command: "dtool bench", MaxMemory: &off}
}

// TestPerfConvertExcelMemory 校验预检倍率 xlsxPeakFactor 没有低估真实开销：
// 峰值堆不得超过「文件大小 × 倍率 × 1.3」。
//
// 两点放宽都刻意为之：一是 HeapAlloc 采样低于 CLI 口径的 RSS（实测 1H2G 下
// 7.7MB xlsx ≈ ×245），二是 GC 时机有抖动。目标是拦住量级回归——比如某次改动让
// 转换内存翻倍，而预检仍按 260 倍估算，用户就会被内核 OOM 静默杀掉。
func TestPerfConvertExcelMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("-short：跳过性能回归")
	}
	dir := t.TempDir()
	env := newPerfEnv(t, dir)
	path, size := writeBigXlsx(t, dir, perfXlsxRows())

	peak := perftest.PeakHeap(func() {
		if _, err := env.Convert(ConvertParams{Input: path, Name: "perf"}, ""); err != nil {
			t.Fatal(err)
		}
	})
	factor := float64(peak) / float64(size)
	limit := float64(size) * xlsxPeakFactor * 1.3
	t.Logf("%d 行 xlsx：文件 %.2fMB，峰值堆 %.1fMB（×%.0f，预检按 ×%d 估算）",
		perfXlsxRows(), perftest.MiB(size), perftest.MiB(peak), factor, xlsxPeakFactor)
	if float64(peak) > limit {
		t.Errorf("转换峰值堆 %.1fMB 超过预检上限 %.1fMB（×%d × 1.3）：请同步调大 xlsxPeakFactor，否则内存不足时用户会被直接 OOM 杀掉",
			perftest.MiB(peak), perftest.MiB(uint64(limit)), xlsxPeakFactor)
	}
}

// BenchmarkConvertExcel 衡量 xlsx → 数据集 的吞吐（含写出 data.json 与 Schema）。
func BenchmarkConvertExcel(b *testing.B) {
	dir := b.TempDir()
	env := newPerfEnv(b, dir)
	path, size := writeBigXlsx(b, dir, perfXlsxRows())
	b.SetBytes(int64(size))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := env.Convert(ConvertParams{Input: path, Name: "perf"}, ""); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(perfXlsxRows()*b.N)/b.Elapsed().Seconds(), "rows/s")
}
