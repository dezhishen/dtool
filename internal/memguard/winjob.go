package memguard

// 这里放 Windows 侧的结构体定义，放在无 build tag 的文件里是为了能在 Linux 上
// 用 unsafe.Offsetof 断言布局——**布局错了不会编译失败，只会读出垃圾值**：
// 上一版 IO_COUNTERS 少写了 OtherTransferCount，ProcessMemoryLimit 就整整错位 8 字节，
// Job Object 的 256MB 上限因此读不到，预检形同虚设（表现为「受限环境下随机 fatal OOM」）。
// 结构体布局以 Windows SDK 为准（64 位）。

const (
	jobObjectExtendedLimitInformationClass = 9
	jobObjectLimitProcessMemory            = 0x100
	jobObjectLimitJobMemory                = 0x2000
)

// JobInfo 是 Windows Job Object 探测的**原始证据**：把「读到什么」和「据此采纳了哪个上限」
// 分开报告，排查「预检为什么没拦住」时一眼能看出是标志位不对、句柄拿不到、还是嵌套 Job。
type JobInfo struct {
	InJob           bool   `json:"in_job"`
	QueryOK         bool   `json:"query_ok"`
	QueryErr        uint32 `json:"query_last_error,omitempty"`
	LimitFlags      uint32 `json:"limit_flags"`
	LimitFlagsHex   string `json:"limit_flags_hex"`
	ProcessMemLimit uint64 `json:"process_memory_limit"`
	JobMemLimit     uint64 `json:"job_memory_limit"`
	UsedLimit       uint64 `json:"used_limit"`
	UsedLimitSource string `json:"used_limit_source,omitempty"`
	TotalPhys       uint64 `json:"total_phys,omitempty"`
	AvailPhys       uint64 `json:"avail_phys,omitempty"`
	UsageNow        uint64 `json:"usage_now,omitempty"`
	Note            string `json:"note,omitempty"`
}

// pickJobLimit 从 LimitFlags 与两个字段里挑出真正生效的上限。
// 两种都认：JOB_OBJECT_LIMIT_PROCESS_MEMORY(0x100) 限单进程提交量；
// JOB_OBJECT_LIMIT_JOB_MEMORY(0x2000) 限整个 job 的提交量（只有本进程时等价），
// 同时设置时取更小的那个。
func pickJobLimit(flags uint32, processLimit, jobLimit uint64) (uint64, string) {
	var limit uint64
	src := ""
	if flags&jobObjectLimitProcessMemory != 0 && processLimit > 0 {
		limit, src = processLimit, "process"
	}
	if flags&jobObjectLimitJobMemory != 0 && jobLimit > 0 {
		if limit == 0 || jobLimit < limit {
			limit, src = jobLimit, "job"
		}
	}
	return limit, src
}

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
