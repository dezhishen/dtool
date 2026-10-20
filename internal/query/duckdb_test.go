//go:build cgo

package query

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dezhishen/dtool/pkg/types"
)

func TestDuckDBRegistered(t *testing.T) {
	eng, err := ResolveEngine("duckdb")
	if err != nil {
		t.Fatal(err)
	}
	if eng.Name() != "duckdb" {
		t.Fatalf("Name() = %q", eng.Name())
	}
	if got := eng.SQLType(KindInteger); got != "BIGINT" {
		t.Fatalf("KindInteger -> %q，期望 BIGINT（DuckDB 的 INTEGER 是 32 位）", got)
	}
	if got := eng.SQLType(KindReal); got != "DOUBLE" {
		t.Fatalf("KindReal -> %q，期望 DOUBLE（REAL 是 float32）", got)
	}
	if got := eng.SQLType(KindText); got != "VARCHAR" {
		t.Fatalf("KindText -> %q", got)
	}
}

func TestDuckDBConfigure(t *testing.T) {
	e := &duckdbEngine{}
	if err := e.Configure(map[string]any{"threads": 2, "memory_limit": "512MB", "temp_directory": "/tmp/dd"}); err != nil {
		t.Fatal(err)
	}
	if e.threads != 2 || e.memoryLimit != "512MB" || e.tempDirectory != "/tmp/dd" {
		t.Fatalf("配置未生效：%+v", e)
	}
	// 数字形式的内存大小按字节字符串交给 DuckDB
	e2 := &duckdbEngine{}
	if err := e2.Configure(map[string]any{"memory_limit": 1048576}); err != nil || e2.memoryLimit != "1048576" {
		t.Fatalf("数字内存：%v %q", err, e2.memoryLimit)
	}
	for _, bad := range []map[string]any{
		{"thread": 2},
		{"threads": 0},
		{"memory_limit": ""},
		{"temp_directory": ""},
	} {
		if err := (&duckdbEngine{}).Configure(bad); err == nil {
			t.Fatalf("非法配置 %v 应报错", bad)
		}
	}
}

func TestDuckDBIsOOM(t *testing.T) {
	e := &duckdbEngine{}
	if !e.IsOOM(errors.New("Out of Memory Error: failed to allocate data of size 1GB")) {
		t.Fatal("DuckDB OOM 文本应被识别")
	}
	if e.IsOOM(nil) || e.IsOOM(errors.New("Catalog Error: Table with name x does not exist")) {
		t.Fatal("非内存错误不该被识别为 OOM")
	}
}

// 端到端：装入 → 查询 → 结果归一。SQLite 的结果是 int64/float64，
// DuckDB 的 SUM(BIGINT) 是 HUGEINT（*big.Int），collect 必须归一成同一种类型。
func TestDuckDBRun(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ints.json")
	if err := os.WriteFile(p, []byte(`[{"n":1,"s":"a"},{"n":2,"s":"b"},{"n":3,"s":"a"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	off := uint64(0)
	res, err := Run(context.Background(), Options{
		SQL:        `SELECT s, SUM(n) AS total, AVG(n) AS avg FROM "d" GROUP BY s ORDER BY s`,
		Sources:    map[string]string{"d": p},
		Sandbox:    true,
		MaxRows:    10,
		MaxMemory:  &off,
		EngineName: "duckdb",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Engine != "duckdb" {
		t.Fatalf("结果应记录 engine=duckdb，实际 %q", res.Engine)
	}
	if res.RowCount != 2 {
		t.Fatalf("行数 = %d，期望 2", res.RowCount)
	}
	if v, _ := res.Rows[0].Get("total"); v != int64(4) {
		t.Fatalf("a 组 SUM = %#v（%T），期望 int64(4)", v, v)
	}
	if v, _ := res.Rows[0].Get("avg"); v != float64(2) {
		t.Fatalf("a 组 AVG = %#v（%T），期望 float64(2)", v, v)
	}
}

// 沙箱：数据由 Go 侧装入，用户 SQL 不应再能读文件；关掉 sandbox 后同一句 SQL
// 必须成功——这样能确认失败确实来自沙箱开关，而不是 SQL 本身写错。
func TestDuckDBSandboxBlocksFileAccess(t *testing.T) {
	dir := t.TempDir()
	csv := filepath.Join(dir, "x.csv")
	if err := os.WriteFile(csv, []byte("a\n1\n2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sqlText := `SELECT COUNT(*) AS n FROM read_csv('` + filepath.ToSlash(csv) + `')`
	off := uint64(0)

	_, err := Run(context.Background(), Options{
		SQL: sqlText, Sandbox: true, MaxRows: 10, MaxMemory: &off, EngineName: "duckdb",
	})
	var te *types.Error
	if !errors.As(err, &te) || te.Code != types.CodeExec {
		t.Fatalf("sandbox 下读文件应报执行错误，实际 %#v", err)
	}
	if !strings.Contains(strings.ToLower(te.Message), "permission") {
		t.Fatalf("错误信息应说明被权限拒绝，实际 %q", te.Message)
	}

	res, err := Run(context.Background(), Options{
		SQL: sqlText, Sandbox: false, MaxRows: 10, MaxMemory: &off, EngineName: "duckdb",
	})
	if err != nil {
		t.Fatalf("关闭 sandbox 后应可读文件：%v", err)
	}
	if v, _ := res.Rows[0].Get("n"); v != int64(2) {
		t.Fatalf("行数 = %#v，期望 2", v)
	}
}

// 磁盘档：DuckDB 落到临时目录里的 .duckdb 文件，结束时清理干净。
func TestDuckDBDiskStore(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "d.json")
	if err := os.WriteFile(p, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	off := uint64(0)
	res, err := Run(context.Background(), Options{
		SQL:        `SELECT COUNT(*) AS n, SUM(amount) AS total FROM "sales"`,
		Sources:    map[string]string{"sales": p},
		Sandbox:    true,
		MaxRows:    10,
		MaxMemory:  &off,
		Store:      "disk",
		EngineName: "duckdb",
	})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := res.Rows[0].Get("n"); v != int64(3) {
		t.Fatalf("行数 = %#v，期望 3", v)
	}
	if v, _ := res.Rows[0].Get("total"); v != float64(400.5) {
		t.Fatalf("SUM = %#v（%T），期望 400.5", v, v)
	}
}
