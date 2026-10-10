//go:build windows

package memguard

import (
	"os"
	"syscall"
	"unsafe"
)

// Windows 上没有 cgroup，但有两种常见的「外部硬上限」需要认出来，否则预检与看门狗
// 全部失效（Available=0 等于不检查），用户只能在进程被 OS 拒绝分配时才看到报错：
//   - 进程被放进 Job Object 并设了 JOB_OBJECT_LIMIT_PROCESS_MEMORY（CI/沙箱常用）；
//   - 系统可用内存。
//
// 两者都拿不到时退化为「不检查」，并在 Source 里说清楚，提示用 --max-memory。
var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	psapi    = syscall.NewLazyDLL("psapi.dll")

	procIsProcessInJob            = kernel32.NewProc("IsProcessInJob")
	procQueryInformationJobObject = kernel32.NewProc("QueryInformationJobObject")
	procGlobalMemoryStatusEx      = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetProcessMemoryInfo      = psapi.NewProc("GetProcessMemoryInfo")
)

const (
	jobObjectExtendedLimitInformation = 9
	jobObjectLimitProcessMemory       = 0x100
)

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
}

type jobObjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type jobObjectExtendedLimitInformationStruct struct {
	BasicLimitInformation jobObjectBasicLimitInformation
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

type processMemoryCounters struct {
	Cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

// jobProcessMemoryLimit 返回当前进程所在 Job Object 的进程内存上限（未设置则为 0）。
func jobProcessMemoryLimit() (uint64, bool) {
	var inJob int32
	if r, _, _ := procIsProcessInJob.Call(0, 0, uintptr(unsafe.Pointer(&inJob))); r == 0 || inJob == 0 {
		return 0, false
	}
	var info jobObjectExtendedLimitInformationStruct
	r, _, _ := procQueryInformationJobObject.Call(0, jobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), 0)
	if r == 0 || info.BasicLimitInformation.LimitFlags&jobObjectLimitProcessMemory == 0 {
		return 0, false
	}
	return uint64(info.ProcessMemoryLimit), true
}

// systemAvailable 返回系统可用物理内存；拿不到时返回 0。
func systemAvailable() uint64 {
	var st memoryStatusEx
	st.Length = uint32(unsafe.Sizeof(st))
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&st))); r == 0 {
		return 0
	}
	return st.AvailPhys
}

// Detect 依次看 DTOOL_MAX_MEMORY、Job Object 上限、系统可用内存。
func Detect() Memory {
	if env, ok := os.LookupEnv("DTOOL_MAX_MEMORY"); ok && env != "" {
		if n, err := ParseBytes(env); err == nil && n > 0 {
			return Memory{Limit: n, Available: n, Source: "环境变量 DTOOL_MAX_MEMORY"}
		}
	}
	sys := systemAvailable()
	used := CurrentUsage()
	if job, ok := jobProcessMemoryLimit(); ok && job > 0 {
		return Memory{Limit: job, Used: used, Available: budgetFrom(job, used, sys),
			Source: "Windows Job Object 进程内存上限"}
	}
	if sys > 0 {
		return Memory{Limit: 0, Used: used, Available: sys, Source: "系统可用内存"}
	}
	return Memory{Source: "未检测（可用 --max-memory 指定）"}
}

// CurrentUsage 返回本进程的工作集（RSS 口径，含非 Go 堆的分配：modernc 的 SQLite
// 页缓存是 mmap 出来的，不计入 Go heap，必须按工作集看）。
func CurrentUsage() uint64 {
	h, _ := syscall.GetCurrentProcess()
	var pmc processMemoryCounters
	pmc.Cb = uint32(unsafe.Sizeof(pmc))
	if r, _, _ := procGetProcessMemoryInfo.Call(uintptr(h),
		uintptr(unsafe.Pointer(&pmc)), unsafe.Sizeof(pmc)); r == 0 {
		return RuntimeUsage()
	}
	return uint64(pmc.WorkingSetSize)
}
