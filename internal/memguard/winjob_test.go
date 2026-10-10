package memguard

import (
	"testing"
	"unsafe"
)

// Windows 结构体布局错了不会编译失败，只会读出垃圾值——上一版就因为 IO_COUNTERS
// 少写 OtherTransferCount，让 ProcessMemoryLimit 错位 8 字节，Job Object 上限读不到，
// 预检形同虚设。这里把偏移量钉死。
func TestWindowsJobStructLayout(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("只在 64 位平台校验（Windows 构建目标为 amd64/arm64）")
	}
	if got, want := unsafe.Sizeof(jobObjectBasicLimitInformation{}), uintptr(64); got != want {
		t.Errorf("JOBOBJECT_BASIC_LIMIT_INFORMATION 大小 = %d, want %d", got, want)
	}
	if got, want := unsafe.Offsetof(jobObjectBasicLimitInformation{}.LimitFlags), uintptr(16); got != want {
		t.Errorf("LimitFlags 偏移 = %d, want %d", got, want)
	}
	if got, want := unsafe.Sizeof(ioCounters{}), uintptr(48); got != want {
		t.Errorf("IO_COUNTERS 大小 = %d, want %d（6 个 ULONGLONG，少一个就错位 8 字节）", got, want)
	}
	info := jobObjectExtendedLimitInformation{}
	if got, want := unsafe.Offsetof(info.IoInfo), uintptr(64); got != want {
		t.Errorf("IoInfo 偏移 = %d, want %d", got, want)
	}
	if got, want := unsafe.Offsetof(info.ProcessMemoryLimit), uintptr(112); got != want {
		t.Errorf("ProcessMemoryLimit 偏移 = %d, want %d", got, want)
	}
	if got, want := unsafe.Sizeof(info), uintptr(144); got != want {
		t.Errorf("JOBOBJECT_EXTENDED_LIMIT_INFORMATION 大小 = %d, want %d", got, want)
	}
	if jobObjectExtendedLimitInformationClass != 9 {
		t.Error("JobObjectExtendedLimitInformation = 9")
	}
	if jobObjectLimitProcessMemory != 0x100 {
		t.Error("JOB_OBJECT_LIMIT_PROCESS_MEMORY = 0x100")
	}
}
