package memguard

// 这里放 Windows 侧的结构体定义，放在无 build tag 的文件里是为了能在 Linux 上
// 用 unsafe.Offsetof 断言布局——**布局错了不会编译失败，只会读出垃圾值**：
// 上一版 IO_COUNTERS 少写了 OtherTransferCount，ProcessMemoryLimit 就整整错位 8 字节，
// Job Object 的 256MB 上限因此读不到，预检形同虚设（表现为「受限环境下随机 fatal OOM」）。
// 结构体布局以 Windows SDK 为准（64 位）。

const (
	jobObjectExtendedLimitInformationClass = 9
	jobObjectLimitProcessMemory            = 0x100
)

// IO_COUNTERS：6 个 ULONGLONG，一个都不能少。
type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobObjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr // DWORD 后有 4 字节填充
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type jobObjectExtendedLimitInformation struct {
	BasicLimitInformation jobObjectBasicLimitInformation
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}
