package memguard

import "runtime"

// RuntimeUsage 返回 Go 从系统申请的内存总量，作为跨平台的内存用量近似值。
func RuntimeUsage() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.Sys
}
