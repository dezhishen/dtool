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
