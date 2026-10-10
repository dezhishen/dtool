package memguard

import "fmt"

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
	InJobCallR1     uint32 `json:"is_process_in_job_r1"`
	InJobCallErr    uint32 `json:"is_process_in_job_last_error,omitempty"`
	LimitUnreadable bool   `json:"limit_unreadable,omitempty"`
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

// jobVerdict 把两次系统调用的原始结果整理成结论：在不在 Job 里、采纳哪个上限、为什么。
//
// 抽成纯函数是有教训的：判定分支只在 Windows 上跑，Linux 的 CI 看不见。
// 上一版把 IsProcessInJob 的返回值当闸门——可它**把「不在任何 Job 里」和「调用失败」
// 都表示成 0**，于是「查询失败」的 note 一出，QueryInformationJobObject 根本没被调用，
// 沙箱里明明设了 256MB 上限，预算却回落到系统可用内存（16.9GB），预检与看门狗全部失效。
// 现在：**以 Job 查询为准**（hJob=NULL 查的就是当前进程所属的 Job，它能成功本身就说明在 Job 里），
// IsProcessInJob 只作为佐证与排错线索留下。
func jobVerdict(inJobR1, inJobErr uint32, queryOK bool, queryErr uint32,
	flags uint32, processLimit, jobLimitBytes uint64) (JobInfo, uint64, string) {
	info := JobInfo{InJobCallR1: inJobR1, InJobCallErr: inJobErr, QueryOK: queryOK, QueryErr: queryErr}
	if !queryOK {
		// 两种情形必须分开说：真的没有 Job（普通桌面），和「有 Job 的迹象却读不到上限」
		// （嵌套 Job / 句柄权限）。前者是正常状态，后者才是需要用户出手的异常——
		// 一律报「读取失败」会让普通桌面用户看到一个不存在的问题。
		if inJobR1 != 0 || inJobErr != 0 {
			info.LimitUnreadable = true
			info.Note = fmt.Sprintf("看起来在 Job Object 里但读不到上限（QueryInformationJobObject last_error=%d，"+
				"IsProcessInJob 返回 %d/err=%d）；预算会回落到系统可用内存，沙箱里请用 --max-memory 显式指定",
				queryErr, inJobR1, inJobErr)
		} else {
			info.Note = "不在任何 Job Object 里（IsProcessInJob 返回 FALSE 且未报错），预算按系统可用内存算"
		}
		return info, 0, ""
	}
	info.InJob = true
	if inJobR1 == 0 {
		info.Note = "IsProcessInJob 返回 FALSE 但 Job 查询成功（0 既表示「不在 Job 里」也表示调用失败），以查询结果为准"
	}
	info.LimitFlags = flags
	info.LimitFlagsHex = fmt.Sprintf("0x%X", flags)
	info.ProcessMemLimit = uint64(processLimit)
	info.JobMemLimit = uint64(jobLimitBytes)
	limit, src := pickJobLimit(flags, processLimit, jobLimitBytes)
	info.UsedLimit, info.UsedLimitSource = limit, src
	if limit == 0 {
		info.Note = joinNote(info.Note, "Job 里没有设置内存上限（LimitFlags 既无 0x100 也无 0x2000）")
	}
	return info, limit, src
}

// joinNote 拼接两条说明，避免出现开头空的分号。
func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	return a + "；" + b
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
