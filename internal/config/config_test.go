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
