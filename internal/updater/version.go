package updater

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Version 对应发版 tag：vX.Y.Z（正式/补丁）或 vX.Y.0-preview.N（预览）。
type Version struct {
	Major, Minor, Patch int
	Pre                 int // 0 表示正式版，>0 为 preview.N
}

var versionRe = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-preview\.([1-9][0-9]*))?$`)

func ParseVersion(s string) (Version, error) {
	m := versionRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Version{}, fmt.Errorf("invalid version %q, want X.Y.Z or X.Y.0-preview.N", s)
	}
	n := func(i int) int { v, _ := strconv.Atoi(m[i]); return v }
	return Version{n(1), n(2), n(3), n(4)}, nil
}

func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre > 0 {
		s += fmt.Sprintf("-preview.%d", v.Pre)
	}
	return s
}

func (v Version) Tag() string      { return "v" + v.String() }
func (v Version) Prerelease() bool { return v.Pre > 0 }

// Compare 返回 -1/0/1；同一 X.Y.Z 下正式版高于任何预览版。
func (v Version) Compare(o Version) int {
	for _, p := range [][2]int{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		if p[0] != p[1] {
			return sign(p[0] - p[1])
		}
	}
	switch {
	case v.Pre == o.Pre:
		return 0
	case v.Pre == 0:
		return 1
	case o.Pre == 0:
		return -1
	}
	return sign(v.Pre - o.Pre)
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}
