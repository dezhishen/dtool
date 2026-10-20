package query

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLiteEngineTypeMapping(t *testing.T) {
	eng := DefaultEngine
	if eng.Name() != "sqlite" {
		t.Fatalf("默认后端应为 sqlite，实际 %q", eng.Name())
	}
	for _, tc := range []struct {
		kind TypeKind
		want string
	}{
		{KindInteger, "INTEGER"},
		{KindReal, "REAL"},
		{KindText, "TEXT"},
	} {
		if got := eng.SQLType(tc.kind); got != tc.want {
			t.Errorf("SQLType(%s) = %q，期望 %q", tc.kind, got, tc.want)
		}
	}
}

func TestSQLiteEngineDetectsOOM(t *testing.T) {
	eng := DefaultEngine
	for _, msg := range []string{"out of memory (7)", "SQLITE_NOMEM"} {
		if !eng.IsOOM(errors.New(msg)) {
			t.Fatalf("%q 应被识别为内存不足", msg)
		}
	}
	if eng.IsOOM(nil) || eng.IsOOM(errors.New("no such table: x")) {
		t.Fatal("非内存错误不该被识别为内存不足")
	}
}

var errFakeOpen = errors.New("fake engine open")

// errEngine 证明 Options.Engine 真的被用于打开数据库：Open 返回哨兵错误。
type errEngine struct {
	sqliteEngine
	opened int
	opts   map[string]any
}

func (e *errEngine) Name() string { return "fake" }

func (e *errEngine) Configure(opts map[string]any) error {
	e.opts = opts
	return nil
}

func (e *errEngine) Open(context.Context, OpenOptions) (*sql.DB, func(), error) {
	e.opened++
	return nil, nil, errFakeOpen
}

func TestRunUsesInjectedEngine(t *testing.T) {
	var _ Engine = (*errEngine)(nil)
	eng := &errEngine{}
	off := uint64(0)
	_, err := Run(context.Background(), Options{SQL: "SELECT 1", MaxMemory: &off, Engine: eng})
	if !errors.Is(err, errFakeOpen) {
		t.Fatalf("应返回注入后端的打开错误，实际 %v", err)
	}
	if eng.opened != 1 {
		t.Fatalf("注入的后端应被打开一次，实际 %d 次", eng.opened)
	}
}

// 配置必须原样送到后端的初始化回调：EngineOpts 就是 db.engine.<name> 那棵子树。
func TestRunPassesEngineOpts(t *testing.T) {
	off := uint64(0)
	eng := &errEngine{}
	_, err := Run(context.Background(), Options{
		SQL: "SELECT 1", MaxMemory: &off, Engine: eng,
		EngineOpts: map[string]any{"threads": 4},
	})
	if !errors.Is(err, errFakeOpen) {
		t.Fatalf("应先配置再打开（Open 的哨兵错误），实际 %v", err)
	}
	if eng.opts == nil || eng.opts["threads"] != 4 {
		t.Fatalf("Configure 应收到 EngineOpts，实际 %#v", eng.opts)
	}
}

// SQLite 的初始化回调：合法键生效、未知键/非法值必须报错（拼错静默忽略最难查）。
func TestSQLiteConfigure(t *testing.T) {
	e := &sqliteEngine{}
	if err := e.Configure(nil); err != nil {
		t.Fatalf("空配置应允许：%v", err)
	}
	if err := e.Configure(map[string]any{"cache_size_kb": 4096, "temp_store": "memory"}); err != nil {
		t.Fatal(err)
	}
	dsn := e.dsn(StoreDisk, "/tmp/x.db")
	for _, want := range []string{"cache_size(-4096)", "temp_store(MEMORY)", "journal_mode(OFF)"} {
		if !strings.Contains(dsn, want) {
			t.Fatalf("配置未生效，DSN 缺 %s：%s", want, dsn)
		}
	}
	e2 := &sqliteEngine{}
	if err := e2.Configure(map[string]any{"cache_size_kb": "2048"}); err != nil || e2.cacheSizeKB != 2048 {
		t.Fatalf("数字字符串应被接受：%v %d", err, e2.cacheSizeKB)
	}
	for _, bad := range []map[string]any{
		{"cach_size_kb": 1},
		{"temp_store": "disk"},
		{"cache_size_kb": -1},
		{"cache_size_kb": 1.5},
	} {
		if err := (&sqliteEngine{}).Configure(bad); err == nil {
			t.Fatalf("非法配置 %v 应报错", bad)
		}
	}
}

// recordEngine 记录装入器询问过的列类型，证明建表走 Engine 而不是写死 SQL 类型名。
type recordEngine struct {
	sqliteEngine
	kinds []TypeKind
}

func (e *recordEngine) SQLType(kind TypeKind) string {
	e.kinds = append(e.kinds, kind)
	return e.sqliteEngine.SQLType(kind)
}

func TestLoadTableAsksEngineForTypes(t *testing.T) {
	var _ Engine = (*recordEngine)(nil)
	dir := t.TempDir()
	path := filepath.Join(dir, "d.json")
	if err := os.WriteFile(path, []byte(`[{"n":1,"s":"x"},{"n":2,"s":"y"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db, cleanup, err := DefaultEngine.Open(ctx, OpenOptions{Store: StoreMemory})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	eng := &recordEngine{}
	if err := loadTable(ctx, eng, db, Binding{Name: "d", Path: path}, LoadStream); err != nil {
		t.Fatalf("装入失败：%v", err)
	}
	if len(eng.kinds) != 2 {
		t.Fatalf("两个列各应询问一次类型，实际 %v", eng.kinds)
	}
	if eng.kinds[0] != KindInteger || eng.kinds[1] != KindText {
		t.Fatalf("列类型推断 = %v，期望 [integer text]", eng.kinds)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM "d"`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("行数 = %d，期望 2", n)
	}
}
