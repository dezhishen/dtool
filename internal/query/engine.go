package query

import (
	"context"
	"database/sql"
	"errors"

	"github.com/dezhishen/dtool/pkg/types"
)

// TypeKind 是装入 JSON 时推断出的列类型，与具体 SQL 引擎无关。
//
// 不让装入器直接产出 SQL 类型名，是因为各引擎的叫法不同（SQLite 的
// INTEGER/REAL/TEXT vs DuckDB 的 BIGINT/DOUBLE/VARCHAR）；一旦把字符串写死，
// 换后端就要改遍 load.go 的每一处。装入器只回答「这是什么」，由
// Engine.SQLType 决定「怎么声明」。
type TypeKind int

const (
	KindText TypeKind = iota
	KindInteger
	KindReal
)

func (k TypeKind) String() string {
	switch k {
	case KindInteger:
		return "integer"
	case KindReal:
		return "real"
	default:
		return "text"
	}
}

// Engine 是查询执行后端的差异面。
//
// dtool 的查询统一走标准 database/sql，装入器产出的也是标准 CREATE TABLE /
// INSERT，所以引擎之间真正不同的只有这几件事：怎么初始化、怎么打开（DSN、
// pragma/SET）、列类型怎么叫、内存不足怎么认、日志与错误里怎么称呼自己。
type Engine interface {
	// Name 返回引擎标识（sqlite / duckdb），用于日志、错误与结果记录。
	Name() string
	// Configure 是初始化回调：每次装配后端时调用一次，opts 是配置文件
	// db.engine.<name> 下的全部键值（nil 表示没有配置）。
	//
	// 约定：未知键必须报错——配置拼错却被静默忽略，是最难查的一类问题；
	// 同一实例上重复调用应当幂等（CLI 启动时先校验一次，真正执行时还会再配一次）。
	Configure(opts map[string]any) error
	// Open 按运行参数打开一个库；返回的 cleanup 负责关闭连接并清理临时文件，
	// 调用方必须 defer 执行。
	Open(ctx context.Context, opts OpenOptions) (*sql.DB, func(), error)
	// SQLType 把装入器推断的类型翻译成该引擎的列类型名。
	SQLType(kind TypeKind) string
	// IsOOM 判断错误是否为引擎侧的内存分配失败，用于翻译成可操作的中文提示。
	IsOOM(err error) bool
}

// OpenOptions 是每次执行的后端运行参数：来自 CLI/运行时，不属于配置文件
// （配置走 Engine.Configure）。
type OpenOptions struct {
	// Store 是库落在内存还是磁盘。
	Store Store
	// Sandbox 开启时，后端必须禁止**执行期**访问任意文件（数据由 Go 侧读入后
	// 以 INSERT 写入，用户 SQL 不需要读文件）。SQLite 内存库天然满足；
	// DuckDB 会关掉 external access。
	Sandbox bool
}

// DefaultEngineName 是默认后端名，与 sqlite.go 里注册的名字一致。
const DefaultEngineName = "sqlite"

// DefaultEngine 是默认后端的零配置实例，供测试与嵌入方直接取用。
//
// 生产路径不要配置它：Run 每次都从注册表的工厂新建实例，配置只落在这次执行的
// 实例上，避免两次执行（或并发执行）之间串味。
var DefaultEngine Engine = &sqliteEngine{}

// engineOf 返回本次执行使用的后端：先按名字/实例定位，再调用初始化回调。
//
// 优先级：显式实例（Options.Engine，测试与嵌入方直接注入）> 名字
// （Options.EngineName，来自 --engine 或配置文件）> DefaultEngine。
// 两者同时给出时要求指向同一个引擎：不一致说明调用方接错了线，早点报错，
// 好过悄悄用错引擎跑出一条看似正常的结果。
func engineOf(o Options) (Engine, error) {
	if o.Engine != nil {
		if normalizeEngineName(o.EngineName) != "" {
			named, err := ResolveEngine(o.EngineName)
			if err != nil {
				return nil, err
			}
			if named.Name() != o.Engine.Name() {
				return nil, types.Errorf(types.CodeUsage,
					"engine conflict: Options.Engine.Name()=%q, Options.EngineName=%q",
					o.Engine.Name(), o.EngineName)
			}
		}
		if err := configure(o.Engine, o.EngineOpts); err != nil {
			return nil, err
		}
		return o.Engine, nil
	}
	return BuildEngine(o.EngineName, o.EngineOpts)
}

// BuildEngine 解析名字并用 opts 初始化，返回可直接使用的后端实例。
// CLI 启动时也用它做一次提前校验：配置写错在第一条命令就报，而不是等查询跑到一半。
func BuildEngine(name string, opts map[string]any) (Engine, error) {
	eng, err := ResolveEngine(name)
	if err != nil {
		return nil, err
	}
	if err := configure(eng, opts); err != nil {
		return nil, err
	}
	return eng, nil
}

// configure 调用初始化回调，并把错误统一成参数错误（配置写错属于使用问题）。
func configure(eng Engine, opts map[string]any) error {
	if err := eng.Configure(opts); err != nil {
		var te *types.Error
		if errors.As(err, &te) {
			return te
		}
		return types.Errorf(types.CodeUsage, "engine %s 配置无效：%v", eng.Name(), err).
			WithHint("检查配置文件 db.engine." + eng.Name() + " 下的键名与取值")
	}
	return nil
}
