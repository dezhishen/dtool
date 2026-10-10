package query

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// 这一组是**策略阶梯的测量台**，默认跳过（不拖慢 go test ./...）：
//
//	DTOOL_GEN_JSON=/tmp/big.json DTOOL_GEN_ROWS=1000000 go test ./internal/query -run TestMeasureGenerate -v
//	DTOOL_MEASURE_JSON=/tmp/big.json go test ./internal/query -run TestMeasureStreamMemory -v
//
// 每个档位必须在**独立进程**里测：VmHWM 是进程级高水位，跑在同一个测试二进制里会
// 只涨不落，第二个档位读到的是前一个的水位。
//
// 峰值口径用 VmHWM（含 Go 堆 + SQLite 页缓存 + runtime 元数据），比 HeapAlloc 更接近
// 用户看到的「进程用掉多少内存」——倍率最终就是要拿它去和 cgroup / Job Object 比。

func TestMeasureGenerate(t *testing.T) {
	out, rows := os.Getenv("DTOOL_GEN_JSON"), os.Getenv("DTOOL_GEN_ROWS")
	if out == "" {
		t.Skip("设置 DTOOL_GEN_JSON（输出路径）与 DTOOL_GEN_ROWS（行数）后运行")
	}
	n, err := strconv.Atoi(rows)
	if err != nil || n <= 0 {
		t.Fatalf("DTOOL_GEN_ROWS = %q", rows)
	}
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	fmt.Fprint(w, "[")
	for i := 0; i < n; i++ {
		if i > 0 {
			fmt.Fprint(w, ",")
		}
		// 仿访问日志：字段多、类型杂，与真实输入的行内形状接近
		fmt.Fprintf(w, `{"ts":"2026-10-%02dT%02d:%02d:%02dZ","ip":"10.%d.%d.%d",`+
			`"method":"GET","path":"/api/v1/items/%d?page=%d","status":%d,"bytes":%d,`+
			`"rt_ms":%d,"ua":"Mozilla/5.0 (compatible; probe/%d)"}`,
			i%28+1, i%24, i%60, i%60, i%256, i%256, i%256, i,
			i%20, []int{200, 201, 404, 500}[i%4], i%100000, i%3000, i%7)
	}
	fmt.Fprint(w, "]")
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	st, _ := f.Stat()
	t.Logf("生成 %s：%d 行，%s", out, n, humanMB(uint64(st.Size())))
}

func TestMeasureFullMemory(t *testing.T)   { measureRung(t, ":memory:", LoadFull) }
func TestMeasureStreamMemory(t *testing.T) { measureRung(t, ":memory:", LoadStream) }

// 磁盘库：SQLite 的页缓存从「进程私有提交」变成「文件页缓存」，可被系统回收，
// 硬上限下这是唯一能真正省出提交量的档。
func TestMeasureStreamDisk(t *testing.T) {
	dir := t.TempDir()
	measureRung(t, "file:"+dir+"/load.db?_pragma=journal_mode(OFF)&_pragma=synchronous(OFF)&_pragma=temp_store(MEMORY)", LoadStream)
}

// 内存库 + 压小页缓存（对比用：看页缓存到底占多少）
func TestMeasureStreamMemoryTight(t *testing.T) {
	measureRung(t, ":memory:?_pragma=cache_size(-512)&_pragma=mmap_size(0)", LoadStream)
}

func measureRung(t *testing.T, dsn string, mode LoadMode) {
	path := os.Getenv("DTOOL_MEASURE_JSON")
	if path == "" {
		t.Skip("设置 DTOOL_MEASURE_JSON 后运行")
	}
	size := uint64(0)
	if st, err := os.Stat(path); err == nil {
		size = uint64(st.Size())
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	before := vmHWM()
	start := time.Now()
	if err := loadTable(ctx, db, Binding{Name: "data", Path: path}, mode); err != nil {
		t.Fatalf("装入失败：%v", err)
	}
	elapsed := time.Since(start)
	peak := vmHWM()
	var rows int
	_ = db.QueryRowContext(ctx, `select count(*) from "data"`).Scan(&rows)

	t.Logf("MEASURE dsn=%s mode=%s input=%s peak=%s ratio=%.2fx rows=%d elapsed=%s",
		dsn, mode, humanMB(size), humanMB(peak), float64(peak)/float64(size), rows, elapsed.Round(time.Millisecond))
	if before > 0 && peak > before {
		t.Logf("  本进程自身增量=%s", humanMB(peak-before))
	}
}

// vmHWM 读 /proc/self/status 的 VmHWM（峰值 RSS）。
func vmHWM() uint64 {
	if runtime.GOOS != "linux" {
		return 0
	}
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "VmHWM:") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			return 0
		}
		kb, _ := strconv.ParseUint(f[1], 10, 64)
		return kb * 1024
	}
	return 0
}

func humanMB(n uint64) string { return fmt.Sprintf("%.0fMB", float64(n)/(1<<20)) }
