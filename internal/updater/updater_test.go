package updater

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dezhishen/dtool/pkg/types"
)

func tarGz(t *testing.T, binName string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	// 与 build-release.sh 一致：条目带 ./ 前缀，并含无关文件
	for _, f := range []struct {
		name string
		data []byte
	}{{"./README.md", []byte("readme")}, {"./" + binName, content}} {
		tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.data)), Typeflag: tar.TypeReg})
		tw.Write(f.data)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func zipOf(t *testing.T, binName string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create(binName)
	w.Write(content)
	zw.Close()
	return buf.Bytes()
}

type rel struct {
	tag        string
	prerelease bool
	draft      bool
	assets     map[string][]byte // 资产名 -> 内容
	badSum     bool
}

// fakeGitHub 提供 releases 列表、按 tag 查询、资产下载。
func fakeGitHub(t *testing.T, rels []rel) *Updater {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	toJSON := func(r rel) map[string]any {
		assets := []map[string]any{}
		for name := range r.assets {
			assets = append(assets, map[string]any{"name": name, "browser_download_url": srv.URL + "/dl/" + r.tag + "/" + name})
		}
		return map[string]any{"tag_name": r.tag, "prerelease": r.prerelease, "draft": r.draft,
			"html_url": srv.URL + "/rel/" + r.tag, "assets": assets}
	}
	mux.HandleFunc("/repos/o/r/releases", func(w http.ResponseWriter, _ *http.Request) {
		list := []map[string]any{}
		for _, r := range rels {
			list = append(list, toJSON(r))
		}
		json.NewEncoder(w).Encode(list)
	})
	for _, r := range rels {
		r := r
		mux.HandleFunc("/repos/o/r/releases/tags/"+r.tag, func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode(toJSON(r))
		})
		for name, data := range r.assets {
			data := data
			mux.HandleFunc("/dl/"+r.tag+"/"+name, func(w http.ResponseWriter, _ *http.Request) { w.Write(data) })
		}
	}
	return &Updater{Repo: "o/r", APIBase: srv.URL, Client: srv.Client(), AllowHTTP: true, OS: "linux", Arch: "amd64"}
}

// release 构造含当前平台资产与 checksums 的发布。
func release(t *testing.T, tag, os_ string, content string, mut ...func(*rel)) rel {
	t.Helper()
	v, _ := ParseVersion(tag)
	r := rel{tag: tag, prerelease: v.Prerelease(), assets: map[string][]byte{}}
	u := &Updater{OS: os_, Arch: "amd64"}
	name := u.assetName(v)
	var arch []byte
	if os_ == "windows" {
		arch = zipOf(t, "dtool.exe", []byte(content))
	} else {
		arch = tarGz(t, "dtool", []byte(content))
	}
	sum := sha256.Sum256(arch)
	r.assets[name] = arch
	r.assets["checksums.txt"] = []byte(fmt.Sprintf("%s  ./%s\n%s  ./other.tar.gz\n", hex.EncodeToString(sum[:]), name, strings.Repeat("0", 64)))
	for _, m := range mut {
		m(&r)
	}
	return r
}

func installedExe(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "dtool")
	if err := os.WriteFile(p, []byte(content), 0o750); err != nil {
		t.Fatal(err)
	}
	return p
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCheck(t *testing.T) {
	u := fakeGitHub(t, []rel{
		{tag: "v1.0.0"}, {tag: "v1.1.0-preview.2", prerelease: true}, {tag: "v1.1.0-preview.1", prerelease: true},
		{tag: "v0.9.0"}, {tag: "v9.9.9", draft: true}, {tag: "nightly"},
	})
	cases := []struct {
		current string
		pre     bool
		latest  string
		avail   bool
	}{
		{"0.9.0", false, "1.0.0", true},
		{"v1.0.0", false, "1.0.0", false},
		{"1.0.0", true, "1.1.0-preview.2", true},
		{"1.1.0-preview.1", true, "1.1.0-preview.2", true},
		{"1.1.0-preview.2", true, "1.1.0-preview.2", false},
		{"dev", false, "1.0.0", true}, // 非发版构建总是提示可升级
		{"2.0.0", false, "1.0.0", false},
	}
	for _, c := range cases {
		u.Current = c.current
		r, err := u.Check(context.Background(), c.pre)
		if err != nil {
			t.Fatal(err)
		}
		if r.Latest != c.latest || r.UpdateAvailable != c.avail || r.Prerelease != strings.Contains(c.latest, "preview") {
			t.Errorf("current=%s pre=%v: %+v", c.current, c.pre, r)
		}
	}
}

func TestCheckStableBeatsSameNumberPreview(t *testing.T) {
	u := fakeGitHub(t, []rel{{tag: "v1.1.0"}, {tag: "v1.1.0-preview.9", prerelease: true}})
	u.Current = "1.0.0"
	if r, _ := u.Check(context.Background(), true); r.Latest != "1.1.0" {
		t.Fatalf("latest = %s", r.Latest)
	}
}

func TestCheckNoReleasesAndAPIErrors(t *testing.T) {
	u := fakeGitHub(t, nil)
	var te *types.Error
	if _, err := u.Check(context.Background(), false); !errors.As(err, &te) || te.Code != types.CodeNotFound {
		t.Fatalf("empty: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer srv.Close()
	u = &Updater{Repo: "o/r", APIBase: srv.URL, Client: srv.Client(), AllowHTTP: true}
	if _, err := u.Check(context.Background(), false); !errors.As(err, &te) || !strings.Contains(te.Hint, "GITHUB_TOKEN") {
		t.Fatalf("rate limit: %v", err)
	}
}

func TestUpgradeReplacesBinaryAndKeepsMode(t *testing.T) {
	u := fakeGitHub(t, []rel{release(t, "v1.1.0", "linux", "NEW-1.1.0"), release(t, "v1.0.0", "linux", "OLD")})
	u.Current, u.Exe = "1.0.0", installedExe(t, "OLD")
	var verified string
	u.Verify = func(p, v string) error { verified = v + ":" + read(t, p); return nil }

	res, err := u.Upgrade(context.Background(), UpgradeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Upgraded || res.From != "1.0.0" || res.To != "1.1.0" || res.Path != u.Exe {
		t.Fatalf("res = %+v", res)
	}
	if read(t, u.Exe) != "NEW-1.1.0" || verified != "1.1.0:NEW-1.1.0" {
		t.Fatalf("exe = %q verified = %q", read(t, u.Exe), verified)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(u.Exe); st.Mode().Perm()&0o700 != 0o700 {
			t.Fatalf("exec bit lost: %v", st.Mode())
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(u.Exe))
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
}

func TestUpgradeNoOpCases(t *testing.T) {
	u := fakeGitHub(t, []rel{release(t, "v1.0.0", "linux", "X"), release(t, "v1.1.0-preview.1", "linux", "P")})
	u.Exe = installedExe(t, "OLD")
	u.Verify = func(string, string) error { t.Fatal("must not install"); return nil }

	for _, c := range []struct{ current, version string }{
		{"1.0.0", ""},           // 已是最新
		{"1.1.0-preview.1", ""}, // 预览版用户不带 --pre：最新正式版更低，不降级
		{"1.0.0", "1.0.0"},      // 指定版本等于当前
	} {
		u.Current = c.current
		res, err := u.Upgrade(context.Background(), UpgradeOptions{Version: c.version})
		if err != nil || res.Upgraded || res.Message == "" {
			t.Errorf("%+v: %+v %v", c, res, err)
		}
	}
	if read(t, u.Exe) != "OLD" {
		t.Fatal("binary modified")
	}
}

func TestUpgradeSpecificVersionAndPre(t *testing.T) {
	u := fakeGitHub(t, []rel{release(t, "v1.0.0", "linux", "V1.0.0"), release(t, "v1.1.0-preview.1", "linux", "PRE")})
	u.Verify = nil

	u.Current, u.Exe = "1.0.0", installedExe(t, "OLD")
	if _, err := u.Upgrade(context.Background(), UpgradeOptions{Pre: true}); err != nil || read(t, u.Exe) != "PRE" {
		t.Fatalf("--pre: %v %q", err, read(t, u.Exe))
	}
	// 指定版本允许降级；带不带 v 均可
	u.Current = "1.1.0-preview.1"
	if res, err := u.Upgrade(context.Background(), UpgradeOptions{Version: "v1.0.0"}); err != nil || !res.Upgraded || read(t, u.Exe) != "V1.0.0" {
		t.Fatalf("downgrade: %+v %v", res, err)
	}
	u.Current = "1.0.0"
	if _, err := u.Upgrade(context.Background(), UpgradeOptions{Version: "1.1.0-preview.1"}); err != nil || read(t, u.Exe) != "PRE" {
		t.Fatalf("explicit preview: %v", err)
	}

	var te *types.Error
	if _, err := u.Upgrade(context.Background(), UpgradeOptions{Version: "9.9.9"}); !errors.As(err, &te) || te.Code != types.CodeNotFound {
		t.Fatalf("unknown version: %v", err)
	}
	if _, err := u.Upgrade(context.Background(), UpgradeOptions{Version: "nope"}); !errors.As(err, &te) || te.Code != types.CodeUsage {
		t.Fatalf("bad version: %v", err)
	}
}

func TestUpgradeDevBuildAlwaysUpgrades(t *testing.T) {
	u := fakeGitHub(t, []rel{release(t, "v1.0.0", "linux", "REL")})
	u.Current, u.Exe = "dev", installedExe(t, "DEV")
	if res, err := u.Upgrade(context.Background(), UpgradeOptions{}); err != nil || !res.Upgraded || read(t, u.Exe) != "REL" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestUpgradeRejectsBadArtifacts(t *testing.T) {
	cases := map[string]func(*rel){
		"checksum mismatch": func(r *rel) {
			for n := range r.assets {
				if strings.HasPrefix(n, "dtool_") {
					r.assets[n] = append([]byte(nil), append(r.assets[n], 0)...)
				}
			}
		},
		"no checksums.txt": func(r *rel) { delete(r.assets, "checksums.txt") },
		"no entry for asset": func(r *rel) {
			r.assets["checksums.txt"] = []byte(strings.Repeat("0", 64) + "  ./other.tar.gz\n")
		},
		"no platform asset": func(r *rel) {
			for n := range r.assets {
				if strings.HasPrefix(n, "dtool_") {
					delete(r.assets, n)
				}
			}
		},
		"binary missing in archive": func(r *rel) {
			for n := range r.assets {
				if strings.HasPrefix(n, "dtool_") {
					r.assets[n] = tarGz(t, "other-name", []byte("x"))
					sum := sha256.Sum256(r.assets[n])
					r.assets["checksums.txt"] = []byte(hex.EncodeToString(sum[:]) + "  ./" + n + "\n")
				}
			}
		},
	}
	for name, mut := range cases {
		u := fakeGitHub(t, []rel{release(t, "v1.1.0", "linux", "NEW", mut)})
		u.Current, u.Exe = "1.0.0", installedExe(t, "OLD")
		if _, err := u.Upgrade(context.Background(), UpgradeOptions{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if read(t, u.Exe) != "OLD" {
			t.Errorf("%s: binary modified", name)
		}
		if entries, _ := os.ReadDir(filepath.Dir(u.Exe)); len(entries) != 1 {
			t.Errorf("%s: leftover files %v", name, entries)
		}
	}
}

func TestUpgradeSelfCheckFailureKeepsOldBinary(t *testing.T) {
	u := fakeGitHub(t, []rel{release(t, "v1.1.0", "linux", "NEW")})
	u.Current, u.Exe = "1.0.0", installedExe(t, "OLD")
	u.Verify = func(string, string) error { return errors.New("boom") }
	if _, err := u.Upgrade(context.Background(), UpgradeOptions{}); err == nil {
		t.Fatal("accepted")
	}
	if read(t, u.Exe) != "OLD" {
		t.Fatal("binary modified")
	}
}

func TestRefusesPlainHTTPUnlessAllowed(t *testing.T) {
	u := fakeGitHub(t, []rel{release(t, "v1.1.0", "linux", "NEW")})
	u.AllowHTTP = false
	u.Current, u.Exe = "1.0.0", installedExe(t, "OLD")
	if _, err := u.Upgrade(context.Background(), UpgradeOptions{}); err == nil {
		t.Fatal("plain http accepted")
	}
}

func TestUpgradeWindowsFlow(t *testing.T) {
	retryDelay = 0
	u := fakeGitHub(t, []rel{release(t, "v1.1.0", "windows", "NEW-EXE")})
	u.OS = "windows"
	dir := t.TempDir()
	u.Current, u.Exe = "1.0.0", filepath.Join(dir, "dtool.exe")
	os.WriteFile(u.Exe, []byte("OLD-EXE"), 0o755)
	// 上一次升级遗留且仍被占用（这里以不可删除的非空目录模拟）的 .old
	locked := u.Exe + ".old"
	os.MkdirAll(filepath.Join(locked, "busy"), 0o755)

	var tmpName string
	u.Verify = func(p, _ string) error { tmpName = filepath.Base(p); return nil }
	res, err := u.Upgrade(context.Background(), UpgradeOptions{})
	if err != nil || !res.Upgraded {
		t.Fatalf("%+v %v", res, err)
	}
	if !strings.HasSuffix(tmpName, ".exe") {
		t.Fatalf("staged file %q must end in .exe to be runnable on Windows", tmpName)
	}
	if read(t, u.Exe) != "NEW-EXE" {
		t.Fatalf("exe = %q", read(t, u.Exe))
	}
	olds, _ := filepath.Glob(u.Exe + ".old.*")
	if len(olds) != 1 || read(t, olds[0]) != "OLD-EXE" {
		t.Fatalf("old binary not moved aside under a fresh name: %v", olds)
	}

	// 下次启动清理
	os.RemoveAll(locked) // 占用解除
	cleanupOld(u.Exe)
	if rest, _ := filepath.Glob(u.Exe + ".old*"); len(rest) != 0 {
		t.Fatalf("cleanup left %v", rest)
	}
	if read(t, u.Exe) != "NEW-EXE" {
		t.Fatal("cleanup touched the live binary")
	}
}

func TestReplaceWindowsRollsBack(t *testing.T) {
	retryDelay = 0
	dir := t.TempDir()
	exe := filepath.Join(dir, "dtool.exe")
	os.WriteFile(exe, []byte("OLD"), 0o755)
	// 新文件不存在 -> 第二次改名失败，必须回滚到旧文件
	if err := replaceExecutable(exe, filepath.Join(dir, "missing.exe"), true); err == nil {
		t.Fatal("expected failure")
	}
	if read(t, exe) != "OLD" {
		t.Fatal("rollback failed")
	}
	if olds, _ := filepath.Glob(exe + ".old*"); len(olds) != 0 {
		t.Fatalf("stale old file after rollback: %v", olds)
	}
}

func TestUpgradeToUnwritableDir(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions are not enforced here")
	}
	u := fakeGitHub(t, []rel{release(t, "v1.1.0", "linux", "NEW")})
	exe := installedExe(t, "OLD")
	os.Chmod(filepath.Dir(exe), 0o555)
	t.Cleanup(func() { os.Chmod(filepath.Dir(exe), 0o755) })
	u.Current, u.Exe = "1.0.0", exe
	_, err := u.Upgrade(context.Background(), UpgradeOptions{})
	var te *types.Error
	if !errors.As(err, &te) || te.Hint == "" {
		t.Fatalf("err = %v", err)
	}
}

func TestExecutableViaSymlinkReplacesTarget(t *testing.T) {
	u := fakeGitHub(t, []rel{release(t, "v1.1.0", "linux", "NEW")})
	real := installedExe(t, "OLD")
	link := filepath.Join(t.TempDir(), "dtool-link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	u.Current, u.Exe = "1.0.0", link
	if _, err := u.Upgrade(context.Background(), UpgradeOptions{}); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Lstat(link); st.Mode()&os.ModeSymlink == 0 || read(t, real) != "NEW" {
		t.Fatal("symlink replaced instead of its target")
	}
}

func TestExpectedSumFormats(t *testing.T) {
	sums := []byte("AAA  ./a.tar.gz\nbbb *b.zip\ninvalid line\nccc  c.tar.gz\n")
	for name, want := range map[string]string{"a.tar.gz": "aaa", "b.zip": "bbb", "c.tar.gz": "ccc"} {
		if got, ok := expectedSum(sums, name); !ok || got != want {
			t.Errorf("%s = %q %v", name, got, ok)
		}
	}
	if _, ok := expectedSum(sums, "d"); ok {
		t.Error("found missing entry")
	}
}

func TestVerifyBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	script := func(out string, code int) string {
		p := filepath.Join(t.TempDir(), "fake")
		os.WriteFile(p, []byte(fmt.Sprintf("#!/bin/sh\necho %q\nexit %d\n", out, code)), 0o755)
		return p
	}
	if err := verifyBinary(script("dtool version 1.1.0", 0), "1.1.0"); err != nil {
		t.Fatalf("good binary rejected: %v", err)
	}
	if err := verifyBinary(script("dtool version 1.0.0", 0), "1.1.0"); err == nil {
		t.Fatal("wrong version accepted")
	}
	if err := verifyBinary(script("boom", 3), "1.1.0"); err == nil {
		t.Fatal("failing binary accepted")
	}
	if err := verifyBinary(filepath.Join(t.TempDir(), "missing"), "1.1.0"); err == nil {
		t.Fatal("missing binary accepted")
	}
}
