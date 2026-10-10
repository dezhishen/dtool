package pipeline

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
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

// perfXlsxRows 是内存回归的默认行数：4 万行（约 1.4MB）能让「整表物化」这种量级
// 回归明显超限（旧实现约 200MB，流式约 22MB），同时只花 2 秒左右；放大用
// DTOOL_BENCH_ROWS。
func perfXlsxRows() int {
	if s := os.Getenv("DTOOL_BENCH_ROWS"); s != "" {
		var n int
		if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return 40000
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

// TestMeasureGenerateXlsx 把 N 行 × 5 列的样本 xlsx 写到指定路径，供**黑盒**边界测试
// （用发布出来的二进制跑 convert）当输入。生成开销与行数成线性。
//
//	DTOOL_GEN_XLSX=/tmp/big.xlsx DTOOL_GEN_ROWS=1048575 go test ./internal/pipeline -run TestMeasureGenerateXlsx -v
func TestMeasureGenerateXlsx(t *testing.T) {
	out, rows := os.Getenv("DTOOL_GEN_XLSX"), os.Getenv("DTOOL_GEN_ROWS")
	if out == "" {
		t.Skip("设置 DTOOL_GEN_XLSX（输出路径）与 DTOOL_GEN_ROWS（行数）后运行")
	}
	n, err := strconv.Atoi(rows)
	if err != nil || n <= 0 {
		t.Fatalf("DTOOL_GEN_ROWS = %q", rows)
	}
	dir := t.TempDir()
	path, size := writeBigXlsx(t, dir, n)
	if err := os.Rename(path, out); err != nil { // 挪出临时目录（同分区，rename 即可）
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if werr := os.WriteFile(out, data, 0o644); werr != nil {
			t.Fatal(werr)
		}
		size = uint64(len(data))
	}
	t.Logf("%d 行 xlsx → %s（%.1fMB）", n, out, float64(size)/(1<<20))
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

// TestPerfConvertExcelMemory 校验预检估算没有低估真实开销：
// 峰值堆不得超过 xlsxNeed(文件大小) × 1.3。
//
// 1.3 是给「HeapAlloc 采样低于 CLI 的 RSS 口径」与 GC 抖动留的余量。目标是拦住
// 量级回归：例如有人重新引入 GetMergeCells 之类的整表物化（4 万行就会多出上百 MB），
// 而预检仍按线性模型估算，用户就会被内核 OOM 静默杀掉。
func TestPerfConvertExcelMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("-short：跳过性能回归")
	}
	dir := t.TempDir()
	env := newPerfEnv(t, dir)
	path, size := writeBigXlsx(t, dir, perfXlsxRows())

	peak := perftest.Measure(func() {
		if _, err := env.Convert(ConvertParams{Input: path, Name: "perf"}, ""); err != nil {
			t.Fatal(err)
		}
	}, perftest.Inclusive).Peak
	need, limit := xlsxNeed(size), float64(xlsxNeed(size))*1.3
	t.Logf("%d 行 xlsx：文件 %.2fMB，峰值堆 %.1fMB（×%.1f），预检估算 %.1fMB",
		perfXlsxRows(), perftest.MiB(size), perftest.MiB(peak), float64(peak)/float64(size), perftest.MiB(need))
	if float64(peak) > limit {
		t.Errorf("转换峰值堆 %.1fMB 超过预检上限 %.1fMB：请同步调大 xlsxBaseOverhead/xlsxPeakFactor，否则内存不足时用户会被直接 OOM 杀掉",
			perftest.MiB(peak), perftest.MiB(uint64(limit)))
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
