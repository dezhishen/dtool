//go:build !cgo

package query

// dtool 只提供多引擎构建：DuckDB 后端依赖 CGO（见 duckdb.go 的 //go:build cgo）。
//
// 这里故意引用一个不存在的标识符，让 CGO_ENABLED=0 在**编译期**失败，
// 而不是悄悄产出一个只有 sqlite 的二进制。
// 正确构建：CGO_ENABLED=1 go build .（Go 的默认值就是 1）。
var _ = error_CGO_ENABLED_1_is_required_for_duckdb
