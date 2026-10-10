package memguard

import (
	"encoding/json"
	"strings"
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

// 这一组是 Bug3 的核心：判定的**顺序**错了，Job Object 上限就会被静默忽略。
// IsProcessInJob 返回 0 既可能是「不在 Job 里」，也可能是调用失败，所以它不能当闸门；
// 只要 Job 查询成功，就必须采信查询到的上限。
func TestJobVerdictTrustsQueryOverIsProcessInJob(t *testing.T) {
	// 复现用户环境：harness 设了 0x100 / 256MB，IsProcessInJob 返回 0（曾因此整条链路被短路）。
	info, limit, src := jobVerdict(0, 0, true, 0, jobObjectLimitProcessMemory, 256<<20, 0)
	if !info.InJob || !info.QueryOK {
		t.Fatalf("Job 查询成功就必须认定在 Job 里：%+v", info)
	}
	if limit != 256<<20 || src != "process" {
		t.Fatalf("上限 = %d/%s, want %d/process", limit, src, 256<<20)
	}
	if info.LimitFlagsHex != "0x100" {
		t.Fatalf("limit_flags_hex = %q", info.LimitFlagsHex)
	}
	if info.InJobCallR1 != 0 {
		t.Fatalf("IsProcessInJob 的原始返回值要留档：%d", info.InJobCallR1)
	}
	if !strings.Contains(info.Note, "IsProcessInJob") {
		t.Fatalf("应说明为何与 IsProcessInJob 不一致：%q", info.Note)
	}
}

func TestJobVerdictBothFlagsPicksSmaller(t *testing.T) {
	info, limit, src := jobVerdict(1, 0, true, 0, 0x2100, 256<<20, 128<<20)
	if limit != 128<<20 || src != "job" {
		t.Fatalf("两个上限都设时应取小：%d/%s", limit, src)
	}
	if info.UsedLimit != limit || info.UsedLimitSource != src {
		t.Fatalf("used_limit 未与采纳结果一致：%+v", info)
	}
}

func TestJobVerdictNoLimitSet(t *testing.T) {
	info, limit, src := jobVerdict(1, 0, true, 0, 0, 0, 0)
	if limit != 0 || src != "" || !info.InJob {
		t.Fatalf("在 Job 里但没设上限：limit=%d src=%q info=%+v", limit, src, info)
	}
	if !strings.Contains(info.Note, "没有设置内存上限") {
		t.Fatalf("note = %q", info.Note)
	}
}

func TestJobVerdictQueryFailureKeepsEvidenceAndHint(t *testing.T) {
	// IsProcessInJob 报了在 Job 里（或调用报错），但上限读不到：这属于必须告警的异常。
	info, limit, src := jobVerdict(1, 87, false, 6, 0x100, 256<<20, 0)
	if limit != 0 || src != "" {
		t.Fatalf("查询失败时不应凭空给出上限：%d/%s", limit, src)
	}
	if !info.LimitUnreadable {
		t.Fatal("有 Job 迹象却读不到上限，必须标记 limit_unreadable（否则普通桌面与沙箱无法区分）")
	}
	if info.QueryErr != 6 || info.InJobCallErr != 87 {
		t.Fatalf("两次调用的 last_error 都要留档：%+v", info)
	}
	for _, want := range []string{"看起来在 Job Object 里但读不到上限", "last_error=6", "--max-memory"} {
		if !strings.Contains(info.Note, want) {
			t.Fatalf("note 缺少 %q：%q", want, info.Note)
		}
	}
	if info.InJob {
		t.Fatal("查询失败时不能声称在 Job 里")
	}
}

// 普通 Windows 桌面（根本不在 Job 里）是正常状态：不能报「读取失败」，
// 否则每台机器都会提示一个不存在的问题，真正的问题反而被淹没。
func TestJobVerdictNoJobIsNotAnError(t *testing.T) {
	info, limit, src := jobVerdict(0, 0, false, 6, 0, 0, 0)
	if limit != 0 || src != "" {
		t.Fatalf("不在 Job 里就没有上限：%d/%s", limit, src)
	}
	if info.LimitUnreadable {
		t.Fatal("不在 Job 里不该报 limit_unreadable")
	}
	if strings.Contains(info.Note, "失败") {
		t.Fatalf("普通桌面不该出现「失败」字样：%q", info.Note)
	}
	if !strings.Contains(info.Note, "不在任何 Job Object 里") {
		t.Fatalf("note = %q", info.Note)
	}
}

// 调用本身出错（errno 非 0）但 Job 查询成功时，以查询结果为准，同时留下线索。
func TestJobVerdictUsesQueryWhenInJobCallErrors(t *testing.T) {
	info, limit, src := jobVerdict(0, 6, true, 0, jobObjectLimitJobMemory, 0, 256<<20)
	if limit != 256<<20 || src != "job" {
		t.Fatalf("应采信查询结果：%d/%s", limit, src)
	}
	if info.InJobCallErr != 6 || !info.QueryOK {
		t.Fatalf("证据不完整：%+v", info)
	}
}

// meminfo 的 job_object 是给人排查用的：原始字段必须真的序列化出去，
// 否则「IsProcessInJob 返回了什么」这类问题只能靠猜。
func TestJobInfoJSONKeepsRawEvidence(t *testing.T) {
	info, _, _ := jobVerdict(0, 5, true, 0, 0x100, 256<<20, 0)
	b, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	for _, want := range []string{`"is_process_in_job_r1":0`, `"is_process_in_job_last_error":5`,
		`"limit_flags_hex":"0x100"`, `"query_ok":true`, `"in_job":true`, `"used_limit_source":"process"`} {
		if !strings.Contains(js, want) {
			t.Fatalf("job_object JSON 缺少 %s：%s", want, js)
		}
	}
}
