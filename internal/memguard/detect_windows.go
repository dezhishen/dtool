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

// jobLimit 读取当前进程所在 Job Object 的上限，并留下**原始证据**（供 meminfo 排查）。
//
// 顺序很重要：**先查 Job，再问 IsProcessInJob**。理由见 jobVerdict 的注释——
// 拿 IsProcessInJob 的返回值当闸门，会把「不在 Job 里」与「调用失败」混为一谈，
// 代价是整条上限探测链路被静默短路（沙箱里设了 256MB，预算却按 16.9GB 算）。
func jobLimit() (JobInfo, uint64, string) {
	// 伪句柄 -1（GetCurrentProcess）而不是 0：不依赖「NULL 表示当前进程」的约定。
	h, _ := syscall.GetCurrentProcess()
	var inJob int32
	r1, _, errno1 := procIsProcessInJob.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&inJob)))
	var inJobR1 uint32
	if r1 != 0 && inJob != 0 {
		inJobR1 = 1
	}

	var je jobObjectExtendedLimitInformation
	r2, _, errno2 := procQueryInformationJobObject.Call(0, jobObjectExtendedLimitInformationClass,
		uintptr(unsafe.Pointer(&je)), unsafe.Sizeof(je), 0)

	queryOK := r2 != 0
	var flags uint32
	var processLimit, jobLimitBytes uint64
	if queryOK {
		flags = je.BasicLimitInformation.LimitFlags
		processLimit, jobLimitBytes = uint64(je.ProcessMemoryLimit), uint64(je.JobMemoryLimit)
	}
	return jobVerdict(inJobR1, errnoCode(errno1), queryOK, errnoCode(errno2), flags, processLimit, jobLimitBytes)
}

// errnoCode 把 syscall 返回的 last error 变成可直接上报的数字；成功时为 0。
func errnoCode(err error) uint32 {
	if err == nil {
		return 0
	}
	if e, ok := err.(syscall.Errno); ok {
		return uint32(e)
	}
	return 0
}

// systemMemory 返回 (物理内存总量, 可用量)；拿不到时返回 0。
func systemMemory() (total, avail uint64) {
	var st memoryStatusEx
	st.Length = uint32(unsafe.Sizeof(st))
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&st))); r == 0 {
		return 0, 0
	}
	return st.TotalPhys, st.AvailPhys
}

// Detect 依次看 DTOOL_MAX_MEMORY、Job Object 上限、系统可用内存。
func Detect() Memory {
	if env, ok := os.LookupEnv("DTOOL_MAX_MEMORY"); ok && env != "" {
		if n, err := ParseBytes(env); err == nil && n > 0 {
			return Memory{Limit: n, Available: n, Source: "环境变量 DTOOL_MAX_MEMORY"}
		}
	}
	total, sys := systemMemory()
	used := CurrentUsage()
	ji, job, src := jobLimit()
	// 上限大于物理内存说明读到的是垃圾（例如结构体布局错位）：宁可退回系统可用内存，
	// 也不要拿一个荒谬的数字让预检形同虚设。
	if job > 0 && (total == 0 || job <= total) {
		source := "Windows Job Object 进程内存上限"
		if src == "job" {
			source = "Windows Job Object 作业内存上限"
		}
		_ = ji
		// Job Object 的进程/作业内存上限是硬上限：撞上去是 runtime fatal（VirtualAlloc
		// 返回 1455），连 recover 都没机会。
		return Memory{Limit: job, Used: used, Available: budgetFrom(job, used, sys), Source: source, Hard: true}
	}
	if sys > 0 {
		source := "系统可用内存"
		if ji.LimitUnreadable {
			// 有 Job 的迹象却读不到上限：这台机器的「预算 16GB」是假的，必须说清楚，
			// 否则预检与看门狗会一起失效，只能等到进程被拒绝分配才暴露。
			source = "系统可用内存（看起来在 Job Object 里但上限读取失败，详见 dtool meminfo）"
		}
		return Memory{Limit: 0, Used: used, Available: sys, Source: source}
	}
	return Memory{Source: "未检测（可用 --max-memory 指定）"}
}

// JobProbe 返回 Windows 侧探测的原始证据（meminfo 用；其他平台返回空值）。
func JobProbe() JobInfo {
	ji, _, _ := jobLimit()
	ji.TotalPhys, ji.AvailPhys = systemMemory()
	ji.UsageNow = CurrentUsage()
	return ji
}

// PeakUsage 返回本进程的**峰值**提交量（PeakPagefileUsage）：记录「这一次实际用了
// 多少」，供选档的历史样本使用（CurrentUsage 是当前值，不是峰值）。
func PeakUsage() uint64 {
	h, _ := syscall.GetCurrentProcess()
	var pmc processMemoryCounters
	pmc.Cb = uint32(unsafe.Sizeof(pmc))
	if r, _, _ := procGetProcessMemoryInfo.Call(uintptr(h),
		uintptr(unsafe.Pointer(&pmc)), unsafe.Sizeof(pmc)); r == 0 {
		return RuntimeUsage()
	}
	return uint64(pmc.PeakPagefileUsage)
}

// CurrentUsage 返回本进程的私有提交量（commit charge）。
//
// 用提交量而不是工作集，是因为 JOB_OBJECT_LIMIT_PROCESS_MEMORY 本身就是**提交上限**：
// 提交会先于工作集触顶（换出的页仍算提交、mmap 出来的 SQLite 页缓存也不在 Go 堆里），
// 按工作集看会眼睁睁看着提交顶到上限才报错。
func CurrentUsage() uint64 {
	h, _ := syscall.GetCurrentProcess()
	var pmc processMemoryCounters
	pmc.Cb = uint32(unsafe.Sizeof(pmc))
	if r, _, _ := procGetProcessMemoryInfo.Call(uintptr(h),
		uintptr(unsafe.Pointer(&pmc)), unsafe.Sizeof(pmc)); r == 0 {
		return RuntimeUsage()
	}
	return uint64(pmc.PagefileUsage)
}
