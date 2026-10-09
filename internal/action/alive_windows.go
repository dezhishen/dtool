//go:build windows

package action

// Windows 下无法廉价探测进程，保守认为仍在运行。
func pidAlive(pid int) bool { return pid > 0 }
