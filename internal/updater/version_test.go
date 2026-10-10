package updater

import "testing"

func TestParseVersion(t *testing.T) {
	ok := map[string]string{"v1.2.3": "1.2.3", "1.2.3": "1.2.3", "v1.2.0-preview.4": "1.2.0-preview.4", " 0.1.0 ": "0.1.0"}
	for in, want := range ok {
		v, err := ParseVersion(in)
		if err != nil || v.String() != want {
			t.Errorf("ParseVersion(%q) = %v, %v", in, v, err)
		}
	}
	for _, in := range []string{"", "dev", "1.2", "v1.2.3.4", "01.2.3", "v1.2.3-rc.1", "v1.2.3-preview.0", "v1.2.3-3-gabc"} {
		if _, err := ParseVersion(in); err == nil {
			t.Errorf("ParseVersion(%q) accepted", in)
		}
	}
}

func TestCompare(t *testing.T) {
	order := []string{"0.9.9", "1.0.0-preview.1", "1.0.0-preview.2", "1.0.0-preview.10", "1.0.0", "1.0.1", "1.1.0-preview.1", "1.1.0", "2.0.0"}
	for i, a := range order {
		for j, b := range order {
			va, _ := ParseVersion(a)
			vb, _ := ParseVersion(b)
			want := sign(i - j)
			if got := va.Compare(vb); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", a, b, got, want)
			}
		}
	}
}

func TestTagAndPrerelease(t *testing.T) {
	v, _ := ParseVersion("1.2.0-preview.3")
	if v.Tag() != "v1.2.0-preview.3" || !v.Prerelease() {
		t.Fatalf("%+v", v)
	}
}

// 零填充是被 semver 明确禁止的（数字标识符不得有前导零），这里把它钉成"必须拒绝"。
//
// 背景：GitHub 的 Releases 列表是按 **tag 名**排序的（不是时间），于是
// v0.2.0-preview.10 会显示在 v0.2.0-preview.9 甚至 v0.2.0-preview.1 的下面，
// 看起来像"没排对"，很容易让人想改成 preview.010 去凑字典序。代价是那个版本对我们
// **完全不可见**：ParseVersion 解析失败 → latest() 静默跳过 → --update / upgrade 永远
// 看不到它，CI 的 release-info.sh 也会直接拒绝这个 tag。
// 列表顺序是展示问题，版本号能不能被解析是功能问题——两者冲突时只能保后者。
func TestPaddedPreviewIsRejected(t *testing.T) {
	for _, in := range []string{"v0.2.0-preview.010", "0.2.0-preview.01", "0.2.0-preview.00"} {
		if v, err := ParseVersion(in); err == nil {
			t.Errorf("ParseVersion(%q) 不该接受（前导零不是合法 semver）：%+v", in, v)
		}
	}
}
