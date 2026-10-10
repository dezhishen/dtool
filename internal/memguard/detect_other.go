//go:build !linux && !windows

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

// PeakUsage 在没有系统级读数的平台退化为 Go 统计（峰值口径不精确，但聊胜于无）。
func PeakUsage() uint64 { return RuntimeUsage() }

// JobProbe 在非 Windows 平台没有 Job Object 可查，返回空值（字段含义见 JobInfo）。
func JobProbe() JobInfo { return JobInfo{Note: "该平台没有 Job Object（Linux 用 cgroup）"} }
