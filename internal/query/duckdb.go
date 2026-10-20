//go:build cgo

// DuckDB 走 CGO 与平台原生 C 库，所以这里用 **cgo** 而不是自定义标签：
//   CGO_ENABLED=1  → 自动包含 DuckDB，任何二进制都同时支持 sqlite 与 duckdb；
//   CGO_ENABLED=0  → 自动排除（受限环境的降级变体，只有 sqlite）。
// 这样「编译出来的每个二进制都有多个引擎」不依赖谁记得加 -tags。
//
// 构建：CGO_ENABLED=1 go build .（默认值就是 1）。

package query

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dezhishen/dtool/pkg/types"
	"github.com/duckdb/duckdb-go/v2"
)

const engineNameDuckDB = "duckdb"

// duckdbEngine 是可选后端：列式分析引擎，适合大文件与复杂聚合。
//
// 数据装入仍然由 Go 侧完成（读 JSON → CREATE TABLE / INSERT），所以沙箱与
// 内存选档逻辑与 SQLite 一致；DuckDB 在装入完成后禁止外部文件访问。
type duckdbEngine struct {
	threads       int    // db.engine.duckdb.threads
	memoryLimit   string // db.engine.duckdb.memory_limit（如 2GB）
	tempDirectory string // db.engine.duckdb.temp_directory
}

var _ Engine = (*duckdbEngine)(nil)

// 自注册成工厂：每次执行新建实例，配置互不串味。
func init() { Register(engineNameDuckDB, func() Engine { return &duckdbEngine{} }) }

func (*duckdbEngine) Name() string { return engineNameDuckDB }

// Configure 读取 db.engine.duckdb 下的配置：
//
//	threads: 4                 并行度（默认由 DuckDB 决定）
//	memory_limit: 2GB          内存上限，超出后溢写到 temp_directory
//	temp_directory: /tmp/dd    溢写目录（默认用本次执行的临时目录）
//
// 未知键直接报错：配置拼错却静默用默认值，排查成本极高。
func (e *duckdbEngine) Configure(opts map[string]any) error {
	for k, v := range opts {
		key := strings.ToLower(strings.TrimSpace(k))
		switch key {
		case "threads":
			n, err := intOption(key, v)
			if err != nil {
				return err
			}
			if n < 1 {
				return fmt.Errorf("threads 必须 ≥ 1，实际 %d", n)
			}
			e.threads = n
		case "memory_limit":
			if s, ok := v.(string); ok {
				if strings.TrimSpace(s) == "" {
					return fmt.Errorf("memory_limit 不能为空")
				}
				e.memoryLimit = strings.TrimSpace(s)
				continue
			}
			n, err := intOption(key, v)
			if err != nil {
				return fmt.Errorf("memory_limit 应为大小字符串（如 2GB）或字节数")
			}
			if n <= 0 {
				return fmt.Errorf("memory_limit 必须 > 0，实际 %d", n)
			}
			e.memoryLimit = strconv.Itoa(n)
		case "temp_directory":
			s, ok := v.(string)
			if !ok || strings.TrimSpace(s) == "" {
				return fmt.Errorf("temp_directory 应为非空路径，实际 %T", v)
			}
			e.tempDirectory = strings.TrimSpace(s)
		default:
			return fmt.Errorf("未知键 %s（支持：threads、memory_limit、temp_directory）", key)
		}
	}
	return nil
}

// SQLType：装入器只推断整数/数值/文本三类。BIGINT 保证 64 位整数不丢精度，
// DOUBLE 对应 KindReal（DuckDB 的 REAL 是 float32，不能用）。
func (*duckdbEngine) SQLType(kind TypeKind) string {
	switch kind {
	case KindInteger:
		return "BIGINT"
	case KindReal:
		return "DOUBLE"
	default:
		return "VARCHAR"
	}
}

// IsOOM 识别 DuckDB 的内存分配失败（如 "Out of Memory Error: failed to allocate ..."）。
func (*duckdbEngine) IsOOM(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "out of memory") ||
		strings.Contains(msg, "failed to allocate") ||
		strings.Contains(msg, "could not allocate")
}

// Open 打开一个 DuckDB 库。
//
// 这里刻意新建一个临时目录：DuckDB 的 temp_directory 默认是进程 CWD 下的
// .tmp（内存库）或数据库文件旁（磁盘库），不设的话溢出文件会落进用户工作区。
// 连接级设置通过 NewConnector 的 boot 回调下发，保证每条连接都带上。
func (e *duckdbEngine) Open(ctx context.Context, opts OpenOptions) (*sql.DB, func(), error) {
	dir, err := os.MkdirTemp("", "dtool-duckdb-")
	if err != nil {
		return nil, nil, types.Errorf(types.CodeGeneral, "创建临时目录失败：%v", err).
			WithHint("DuckDB 需要可写临时目录（TMPDIR）；也可用 --store memory 减少落盘")
	}
	base := "" // 空路径 = 内存库
	if opts.Store == StoreDisk {
		base = filepath.Join(dir, "load.duckdb")
	}
	tempDir := e.tempDirectory
	if tempDir == "" {
		tempDir = dir
	}
	cleanup := func() {
		_ = os.RemoveAll(dir)
	}

	dsn := base
	params := url.Values{}
	if e.threads > 0 {
		params.Set("threads", strconv.Itoa(e.threads))
	}
	if e.memoryLimit != "" {
		params.Set("memory_limit", e.memoryLimit)
	}
	if q := params.Encode(); q != "" {
		dsn += "?" + q
	}

	boot := func(ex driver.ExecerContext) error {
		// temp_directory 是连接级设置，且必须在关闭外部访问**之前**设置；
		// 之后的连接再设会被拒绝（实例上已经是这个值），所以只放过这一种错误。
		if _, err := ex.ExecContext(ctx, "SET temp_directory = "+duckQuote(tempDir), nil); err != nil &&
			!strings.Contains(err.Error(), "temp_directory") {
			return err
		}
		if opts.Sandbox {
			// 数据已经用 Go 读完并以 INSERT 写入，执行期不需要读文件：
			// read_csv / read_parquet / read_json / glob 等一律被拒绝。
			if _, err := ex.ExecContext(ctx, "SET enable_external_access = false", nil); err != nil {
				return err
			}
		}
		return nil
	}
	conn, err := duckdb.NewConnector(dsn, boot)
	if err != nil {
		cleanup()
		return nil, nil, types.Errorf(types.CodeGeneral, "打开 DuckDB 失败：%v", err)
	}
	db := sql.OpenDB(conn)
	// 单连接：SET 是连接级设置，装入也是单线程批量 INSERT。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	// 先建一次连接，让 memory_limit 等配置错误在 Open 阶段就暴露出来。
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		cleanup()
		return nil, nil, types.Errorf(types.CodeUsage, "DuckDB 配置无效：%v", err).
			WithHint("检查配置文件 db.engine.duckdb 下的 threads / memory_limit / temp_directory")
	}
	return db, func() {
		_ = db.Close()
		cleanup()
	}, nil
}

// duckQuote 生成 SQL 字符串字面量（路径里的单引号转义成两个）。
func duckQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
