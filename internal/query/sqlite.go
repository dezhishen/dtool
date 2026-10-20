package query

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dezhishen/dtool/pkg/types"
	_ "modernc.org/sqlite" // 注册驱动：纯 Go，CGO_ENABLED=0 也能编译
)

// sqliteEngine 是默认后端：一次查询一个临时 SQLite 库（内存或磁盘），
// 由 modernc.org/sqlite（纯 Go）实现，不依赖任何外部进程或 C 库。
//
// 字段来自配置文件 db.engine.sqlite.*，由 Configure 写入；零值即历史默认行为。
type sqliteEngine struct {
	cacheSizeKB int    // 页缓存 KiB；0 表示磁盘库用默认 8MB、内存库不设
	tempStore   string // FILE / MEMORY；空表示 FILE
}

var _ Engine = (*sqliteEngine)(nil)

// 自注册成工厂：每次解析都得到新实例，配置不会在两次执行之间串味。
func init() { Register(DefaultEngineName, func() Engine { return &sqliteEngine{} }) }

// db.engine.sqlite.* 支持的全部键。
const (
	optCacheSizeKB = "cache_size_kb"
	optTempStore   = "temp_store"
)

func (*sqliteEngine) Name() string { return DefaultEngineName }

// Configure 读取 db.engine.sqlite 下的配置：
//
//	cache_size_kb: 8192      页缓存（KiB）；磁盘库默认 8000，内存库默认不设
//	temp_store: file|memory  临时表落哪；默认 file（理由见 storeDSN 注释）
//
// 未知键直接报错：配置拼错却静默用默认值，排查成本极高。
func (e *sqliteEngine) Configure(opts map[string]any) error {
	for k, v := range opts {
		key := strings.ToLower(strings.TrimSpace(k))
		switch key {
		case optCacheSizeKB:
			n, err := intOption(key, v)
			if err != nil {
				return err
			}
			if n < 0 {
				return fmt.Errorf("%s 不能为负（单位 KiB，例如 8192）", key)
			}
			e.cacheSizeKB = n
		case optTempStore:
			s, ok := v.(string)
			if !ok {
				return fmt.Errorf("%s 应为 file 或 memory，实际 %T", key, v)
			}
			switch strings.ToLower(strings.TrimSpace(s)) {
			case "file":
				e.tempStore = "FILE"
			case "memory":
				// 允许，但会把 GROUP BY/ORDER BY 的排序器留在内存里（实测聚合段
				// +42MB vs +5MB）；--store disk 想省峰值就别设成 memory。
				e.tempStore = "MEMORY"
			default:
				return fmt.Errorf("%s 只能是 file 或 memory，实际 %q", key, s)
			}
		default:
			return fmt.Errorf("未知键 %s（支持：%s、%s）", key, optCacheSizeKB, optTempStore)
		}
	}
	return nil
}

// SQLType 保持与历史建表口径完全一致：装入器只推断整数/数值/文本三类
// （布尔按 0/1 存，嵌套对象与数组按 JSON 文本存）。
func (*sqliteEngine) SQLType(kind TypeKind) string {
	switch kind {
	case KindInteger:
		return "INTEGER"
	case KindReal:
		return "REAL"
	default:
		return "TEXT"
	}
}

// IsOOM 识别 modernc/sqlite 的分配失败（SQLITE_NOMEM，驱动文本是
// "out of memory (7)"）。它的页缓存是 mmap 出来的，不计入 Go 堆，所以当进程顶到
// 外部硬上限（Windows Job Object、cgroup、ulimit）时会直接返回这个错误——此时
// debug.SetMemoryLimit 帮不上忙，只有 --max-memory 预检与看门狗能提前拦住。
func (*sqliteEngine) IsOOM(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "out of memory") || strings.Contains(msg, "sqlite_nomem")
}

// Open 打开装载与查询用的数据库。磁盘库返回的清理函数会关闭连接并删除临时文件：
// 刻意用系统临时目录而不是工作区——它是过程产物，不该留在 .dtool 里。
func (e *sqliteEngine) Open(ctx context.Context, opts OpenOptions) (*sql.DB, func(), error) {
	st := opts.Store
	dsn, dir := e.dsn(st, ""), ""
	if st == StoreDisk {
		d, err := os.MkdirTemp("", "dtool-load-")
		if err != nil {
			return nil, nil, types.Errorf(types.CodeGeneral, "创建临时目录失败：%v", err).
				WithHint("磁盘库需要可写的临时目录（TMPDIR）；也可用 --store memory 换回内存库")
		}
		dir = d
		dsn = e.dsn(st, filepath.Join(dir, "load.db"))
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
		return nil, nil, err
	}
	db.SetMaxOpenConns(1) // 内存库按连接隔离；磁盘库同样保持单连接以免竞争
	cleanup := func() {
		_ = db.Close()
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
	}
	return db, cleanup, nil
}

// dsn 按配置生成连接串。
//
// 两处 pragma 都是有来历的（1 核 + cgroup，123MB / 70 万行，`GROUP BY <高基数列>`）：
//
//	journal_mode(OFF)+synchronous(OFF)  磁盘库是一次性产物，掉电丢失无所谓，省掉 fsync
//	temp_store(FILE)                   **临时表必须落盘**：GROUP BY/ORDER BY 在没有索引时
//	                                   要先排序，temp_store=MEMORY 会把排序器整个塞进内存
//	                                   ——实测聚合段从 +5MB 涨到 +42MB，而磁盘库的加载段
//	                                   本来只有 33MB，等于白省；改成 FILE 后总峰值
//	                                   75MB → 52MB，耗时不变（1.78s vs 1.79s）。
//	                                   这一条同时解释了 Windows 上「disk 档不降峰值」：
//	                                   不是磁盘库没用，是排序器在内存里顶着。
//	cache_size(-8000)                 页缓存 8MB（磁盘库），配合落盘的临时表把提交量压低
func (e *sqliteEngine) dsn(st Store, dbPath string) string {
	pragmas := []string{"temp_store(" + e.tempStoreName() + ")"}
	if st == StoreDisk {
		pragmas = append([]string{"journal_mode(OFF)", "synchronous(OFF)"}, pragmas...)
		switch {
		case e.cacheSizeKB > 0:
			pragmas = append(pragmas, fmt.Sprintf("cache_size(-%d)", e.cacheSizeKB))
		default:
			pragmas = append(pragmas, "cache_size(-8000)")
		}
	} else if e.cacheSizeKB > 0 {
		pragmas = append(pragmas, fmt.Sprintf("cache_size(-%d)", e.cacheSizeKB))
	}
	q := make([]string, len(pragmas))
	for i, p := range pragmas {
		q[i] = "_pragma=" + p
	}
	base := ":memory:"
	if st == StoreDisk {
		base = "file:" + filepath.ToSlash(dbPath)
	}
	return base + "?" + strings.Join(q, "&")
}

func (e *sqliteEngine) tempStoreName() string {
	if e.tempStore == "" {
		return "FILE"
	}
	return e.tempStore
}

// storeDSN 是默认配置下的连接串，供预演/测试引用。
func storeDSN(st Store, dbPath string) string { return (&sqliteEngine{}).dsn(st, dbPath) }

// intOption 接受 YAML 解出来的 int / int64 / uint64 / float64（整数值）/ 数字字符串。
func intOption(name string, v any) (int, error) {
	switch x := v.(type) {
	case int:
		return x, nil
	case int64:
		return int(x), nil
	case uint64:
		return int(x), nil
	case float64:
		if x != math.Trunc(x) {
			return 0, fmt.Errorf("%s 应为整数，实际 %v", name, x)
		}
		return int(x), nil
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		if err != nil {
			return 0, fmt.Errorf("%s 应为整数，实际 %q", name, x)
		}
		return n, nil
	}
	return 0, fmt.Errorf("%s 应为整数，实际 %T", name, v)
}
