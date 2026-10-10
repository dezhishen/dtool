//go:build linux

package memguard

import "testing"

// 没有 cgroup 上限时也必须回退到系统可用内存：曾经这里算出 0，等于「预检与看门狗
// 全关」，普通机器上根本发现不了。
func TestDetectFallsBackToSystemAvailable(t *testing.T) {
	t.Setenv("DTOOL_MAX_MEMORY", "")
	m := Detect()
	avail := memAvailable()
	if m.Limit == 0 && avail > 0 && m.Available == 0 {
		t.Fatalf("系统可用 %s，但预算为 0（预检/看门狗会全关）", HumanSize(avail))
	}
}
