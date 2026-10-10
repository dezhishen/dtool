//go:build linux

package memguard

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// detectMemory 读取 cgroup v2/v1 限制与 /proc/meminfo，得出可用内存预算。
func Detect() Memory {
	var m Memory
	if env, ok := os.LookupEnv("DTOOL_MAX_MEMORY"); ok && env != "" {
		if n, err := ParseBytes(env); err == nil && n > 0 {
			return Memory{Limit: n, Available: n, Source: "环境变量 DTOOL_MAX_MEMORY"}
		}
	}
	limit, used, src := cgroupMemory()
	avail := memAvailable()
	m = Memory{Limit: limit, Used: used, Source: src}
	// 取 min(cgroup 上限-已用, 系统可用)；没有 cgroup 上限时就用系统可用内存。
	// 这里曾经写成 min(m.Available, avail) 且 m.Available 在没有 cgroup 时是 0，
	// 结果「普通机器上预检与看门狗全是关的」——探针（meminfo）才把它照出来。
	m.Available = budgetFrom(limit, used, avail)
	if limit == 0 {
		m.Source = "系统可用内存"
	}
	return m
}

// cgroupMemory 返回 (上限, 已用, 来源)；无限制时上限为 0。
func cgroupMemory() (uint64, uint64, string) {
	// cgroup v2：/proc/self/cgroup 形如 "0::/user.slice/.../session.scope"
	if b, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.SplitN(line, ":", 3)
			if len(f) != 3 || f[0] != "0" || f[1] != "" {
				continue
			}
			dir := filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(f[2], "/"))
			if l, ok := readLimit(filepath.Join(dir, "memory.max")); ok {
				cur, _ := readLimit(filepath.Join(dir, "memory.current"))
				return l, cur, "cgroup v2 内存限制"
			}
		}
	}
	// cgroup v1
	if l, ok := readLimit("/sys/fs/cgroup/memory/memory.limit_in_bytes"); ok {
		cur, _ := readLimit("/sys/fs/cgroup/memory/memory.usage_in_bytes")
		return l, cur, "cgroup v1 内存限制"
	}
	return 0, 0, ""
}

// readLimit 解析 cgroup 数值文件；"max" 或异常大的值（未设限）返回 ok=false。
func readLimit(path string) (uint64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(b))
	if s == "max" {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || n > 1<<62 { // v1 未设限时会写一个巨大的数
		return 0, false
	}
	return n, true
}

func memAvailable() uint64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "MemAvailable:"); ok {
			f := strings.Fields(v)
			if len(f) > 0 {
				if kb, err := strconv.ParseUint(f[0], 10, 64); err == nil {
					return kb * 1024
				}
			}
		}
	}
	return 0
}

// currentUsage 优先读本进程 RSS（不 STW），失败时退回 Go 统计。
func CurrentUsage() uint64 {
	if b, err := os.ReadFile("/proc/self/status"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(line, "VmRSS:"); ok {
				f := strings.Fields(v)
				if len(f) > 0 {
					if kb, err := strconv.ParseUint(f[0], 10, 64); err == nil {
						return kb * 1024
					}
				}
			}
		}
	}
	return RuntimeUsage()
}

// JobProbe：Linux 没有 Job Object（预算来自 cgroup/系统可用内存），返回空值占位。
func JobProbe() JobInfo {
	return JobInfo{Note: "Linux 没有 Job Object，预算来自 cgroup/系统可用内存"}
}
