package buildinfo

import (
	"fmt"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
)

// 以下变量在打包时由 scripts/ldflags.sh 通过 -ldflags "-X <pkg>.Name=value" 注入。
var (
	Version    = "dev"
	Commit     = "" // 完整 commit SHA
	CommitDate = "" // commit 提交时间（ISO 8601）
	Branch     = "" // 构建所在分支
	Dirty      = "" // "true" 表示构建时工作区有未提交改动
	BuildDate  = "" // 构建时间（UTC, RFC3339）
	BuildID    = "" // CI 运行 ID（GitHub Actions run id）
	BuildURL   = "" // CI 运行页面
	Builder    = "" // github-actions / local
	Repo       = "https://github.com/dezhishen/dtool"
)

type Module struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

type Info struct {
	Version    string   `json:"version"`
	Channel    string   `json:"channel"` // stable / preview / dev / local
	Commit     string   `json:"commit"`
	CommitDate string   `json:"commit_date"`
	Branch     string   `json:"branch"`
	Dirty      bool     `json:"dirty"`
	BuildDate  string   `json:"build_date"`
	BuildID    string   `json:"build_id"`
	BuildURL   string   `json:"build_url"`
	Builder    string   `json:"builder"`
	Repo       string   `json:"repo"`
	Go         string   `json:"go"`
	Compiler   string   `json:"compiler"`
	CGO        bool     `json:"cgo"`
	OS         string   `json:"os"`
	Arch       string   `json:"arch"`
	Deps       []Module `json:"deps,omitempty"`
}

var (
	stableRe  = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)
	previewRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+-preview\.\d+$`)
)

// Channel 由版本号判定：X.Y.Z 为 stable（含补丁版），X.Y.0-preview.N 为 preview，dev-* 为 dev，其余为 local。
func Channel(version string) string {
	switch {
	case stableRe.MatchString(version):
		return "stable"
	case previewRe.MatchString(version):
		return "preview"
	case strings.HasPrefix(version, "dev-"):
		return "dev"
	}
	return "local"
}

// Get 汇总构建元数据；未注入的字段回退到 Go 嵌入的 VCS 信息（go build / go install 产物）。
func Get() Info {
	i := Info{Version: Version, Commit: Commit, CommitDate: CommitDate, Branch: Branch, Dirty: Dirty == "true",
		BuildDate: BuildDate, BuildID: BuildID, BuildURL: BuildURL, Builder: Builder, Repo: Repo,
		Go: runtime.Version(), Compiler: runtime.Compiler, OS: runtime.GOOS, Arch: runtime.GOARCH}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if i.Version == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			i.Version = strings.TrimPrefix(bi.Main.Version, "v") // go install ...@v1.2.0
		}
		injected := Commit != ""
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if i.Commit == "" {
					i.Commit = s.Value
				}
			case "vcs.time":
				if i.CommitDate == "" {
					i.CommitDate = s.Value
				}
			case "vcs.modified":
				if !injected {
					i.Dirty = s.Value == "true"
				}
			case "CGO_ENABLED":
				i.CGO = s.Value == "1"
			}
		}
		for _, d := range bi.Deps {
			v := d.Version
			if d.Replace != nil {
				v = d.Replace.Version
			}
			i.Deps = append(i.Deps, Module{Path: d.Path, Version: v})
		}
	}
	if i.Builder == "" {
		i.Builder = "local"
	}
	i.Channel = Channel(i.Version)
	return i
}

// ShortCommit 取前 7 位，脏工作区追加 -dirty。
func (i Info) ShortCommit() string {
	c := i.Commit
	if len(c) > 7 {
		c = c[:7]
	}
	if c == "" {
		return "unknown"
	}
	if i.Dirty {
		c += "-dirty"
	}
	return c
}

// Summary 形如 "1.2.0 (commit abc1234, built 2026-10-09T01:00:00Z, go1.27.1 linux/amd64)"。
func (i Info) Summary() string {
	built := i.BuildDate
	if built == "" {
		built = "unknown"
	}
	return fmt.Sprintf("%s (commit %s, built %s, %s %s/%s)", i.Version, i.ShortCommit(), built, i.Go, i.OS, i.Arch)
}
