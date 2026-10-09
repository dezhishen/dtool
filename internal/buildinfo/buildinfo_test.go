package buildinfo

import (
	"strings"
	"testing"
)

func TestChannel(t *testing.T) {
	cases := map[string]string{
		"1.2.0": "stable", "v1.2.3": "stable", "1.2.0-preview.4": "preview",
		"dev-123456": "dev", "dev": "local", "": "local", "1.2.3-3-gabc": "local",
	}
	for in, want := range cases {
		if got := Channel(in); got != want {
			t.Errorf("Channel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShortCommit(t *testing.T) {
	cases := []struct {
		commit string
		dirty  bool
		want   string
	}{
		{"", false, "unknown"}, {"abc", false, "abc"}, {"0123456789abcdef", false, "0123456"},
		{"0123456789abcdef", true, "0123456-dirty"},
	}
	for _, c := range cases {
		if got := (Info{Commit: c.commit, Dirty: c.dirty}).ShortCommit(); got != c.want {
			t.Errorf("ShortCommit(%q, %v) = %q, want %q", c.commit, c.dirty, got, c.want)
		}
	}
}

func TestSummary(t *testing.T) {
	s := Info{Version: "dev-123456", Commit: "deadbeefcafe", BuildDate: "2026-10-09T01:00:00Z", Go: "go1.27", OS: "linux", Arch: "amd64"}.Summary()
	for _, want := range []string{"dev-123456", "commit deadbee", "built 2026-10-09T01:00:00Z", "go1.27 linux/amd64"} {
		if !strings.Contains(s, want) {
			t.Errorf("%q missing %q", s, want)
		}
	}
	if !strings.Contains((Info{Version: "1.0.0"}).Summary(), "built unknown") {
		t.Error("missing build date should render as unknown")
	}
}

func TestGetUsesInjectedValues(t *testing.T) {
	old := []string{Version, Commit, CommitDate, Branch, Dirty, BuildDate, BuildID, BuildURL, Builder}
	defer func() {
		Version, Commit, CommitDate, Branch, Dirty, BuildDate, BuildID, BuildURL, Builder =
			old[0], old[1], old[2], old[3], old[4], old[5], old[6], old[7], old[8]
	}()
	Version, Commit, CommitDate, Branch, Dirty = "dev-42", "feedface00", "2030-01-01T00:00:00+08:00", "main", "true"
	BuildDate, BuildID, BuildURL, Builder = "2030-01-02T00:00:00Z", "42", "https://example/runs/42", "github-actions"

	i := Get()
	if i.Version != "dev-42" || i.Channel != "dev" || i.Commit != "feedface00" || i.CommitDate != "2030-01-01T00:00:00+08:00" ||
		i.Branch != "main" || !i.Dirty || i.BuildDate != "2030-01-02T00:00:00Z" || i.BuildID != "42" ||
		i.BuildURL != "https://example/runs/42" || i.Builder != "github-actions" || i.Repo == "" {
		t.Fatalf("%+v", i)
	}
	if i.Go == "" || i.Compiler == "" || i.OS == "" || i.Arch == "" {
		t.Fatalf("runtime fields missing: %+v", i)
	}
	if len(i.Deps) == 0 {
		t.Log("no module deps recorded (test binary)")
	}
}

func TestBuilderDefaultsToLocal(t *testing.T) {
	old := Builder
	defer func() { Builder = old }()
	Builder = ""
	if Get().Builder != "local" {
		t.Fatal("builder should default to local")
	}
}
