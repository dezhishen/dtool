//go:build !linux

package memguard

import (
	"os"
)

// Detect：非 Linux 平台只认显式配置，拿不到就退化为不检查。
func Detect() Memory {
	if env, ok := os.LookupEnv("DTOOL_MAX_MEMORY"); ok && env != "" {
		if n, err := ParseBytes(env); err == nil && n > 0 {
			return Memory{Limit: n, Available: n, Source: "环境变量 DTOOL_MAX_MEMORY"}
		}
	}
	return Memory{Source: "未检测（该平台不支持自动探测，可用 --max-memory 指定）"}
}

func CurrentUsage() uint64 { return RuntimeUsage() }
