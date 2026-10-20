package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dezhishen/dtool/pkg/types"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadResolvesRelativeToConfigDir(t *testing.T) {
	p := write(t, "font: fonts/msyh.ttf\nworkspace: data/.dtool\npreview_rows: 5\n")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(p)
	if c.Font != filepath.Join(dir, "fonts/msyh.ttf") || c.Workspace != filepath.Join(dir, "data/.dtool") || c.PreviewRows != 5 {
		t.Fatalf("%+v", c)
	}
}

func TestLoadAbsoluteAndHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	abs := filepath.Join(t.TempDir(), "f.ttf")
	c, err := Load(write(t, "font: "+abs+"\nworkspace: ~/ws\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Font != abs {
		t.Fatalf("font = %s", c.Font)
	}
	if home != "" && c.Workspace != filepath.Join(home, "ws") {
		t.Fatalf("workspace = %s", c.Workspace)
	}
}

func TestLoadModeField(t *testing.T) {
	c, err := Load(write(t, "load_mode: stream\n"))
	if err != nil || c.LoadMode != "stream" {
		t.Fatalf("load_mode: %+v %v", c, err)
	}
}

func TestParsePrivacy(t *testing.T) {
	for _, s := range []string{"", "strong", "STRONG", " normal "} {
		p, err := ParsePrivacy(s)
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		want := PrivacyStrong
		if strings.TrimSpace(strings.ToLower(s)) == "normal" {
			want = PrivacyNormal
		}
		if p != want {
			t.Fatalf("%q = %q, want %q", s, p, want)
		}
	}
	if p, err := ParsePrivacy("weak"); err == nil || p != "" {
		t.Fatalf("weak should fail: %q %v", p, err)
	} else if te := types.AsError(err, 0); te.Code != types.CodeUsage {
		t.Fatalf("code = %d", te.Code)
	}
}

func TestLoadEmptyAndErrors(t *testing.T) {
	if c, err := Load(write(t, "")); err != nil || c.Font != "" {
		t.Fatalf("empty file: %+v %v", c, err)
	}
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	var te *types.Error
	if !errors.As(err, &te) || te.Code != types.CodeNotFound {
		t.Fatalf("missing: %v", err)
	}
	_, err = Load(write(t, "fnot: x\n"))
	if !errors.As(err, &te) || te.Code != types.CodeUsage || !strings.Contains(te.Message, "fnot") {
		t.Fatalf("unknown key should be rejected: %v", err)
	}
	if _, err = Load(write(t, "font: [")); err == nil {
		t.Fatal("malformed yaml accepted")
	}
}

func TestEngineConfigForms(t *testing.T) {
	// 分组形式：db.engine.<name> 下的整棵子树
	c, err := Load(write(t, `
db:
  engine:
    sqlite:
      cache_size_kb: 8192
    duckdb:
      threads: 4
      extensions: [json]
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.DB.Engine.For("SQLite"); got["cache_size_kb"] != 8192 {
		t.Fatalf("sqlite 配置 = %#v", got)
	}
	if got := c.DB.Engine.For("duckdb"); got["threads"] != 4 {
		t.Fatalf("duckdb 配置 = %#v", got)
	}
	if c.DB.Engine.For("missing") != nil {
		t.Fatal("没有配置的引擎应返回 nil")
	}

	// 简写形式：db.engine: sqlite（只选后端）
	c, err = Load(write(t, "db:\n  engine: sqlite\n"))
	if err != nil || c.DB.Engine.Name != "sqlite" {
		t.Fatalf("简写形式：%+v %v", c.DB, err)
	}

	// name + 分组：既选后端又保留配置
	c, err = Load(write(t, `
db:
  engine:
    name: duckdb
    duckdb:
      threads: 8
`))
	if err != nil || c.DB.Engine.Name != "duckdb" || c.DB.Engine.For("duckdb")["threads"] != 8 {
		t.Fatalf("name+分组：%+v %v", c.DB, err)
	}

	// 非法形态（既不是字符串也不是键值表）必须报错，别静默当没配置
	if _, err := Load(write(t, "db:\n  engine: [sqlite]\n")); err == nil {
		t.Fatal("db.engine 为列表时应报错")
	}
}
