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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dezhishen/dtool/pkg/types"
)

const skillsDoc = "SKILLS-DOC"

type namedFile struct {
	name string
	data []byte
}

func tarGzFiles(t *testing.T, files ...namedFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.data)), Typeflag: tar.TypeReg})
		tw.Write(f.data)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// tarGz 与 build-release.sh 的归档布局一致：条目带 ./ 前缀，且含 README/skills.md 等非二进制文件。
func tarGz(t *testing.T, binName string, content []byte) []byte {
	t.Helper()
	return tarGzFiles(t,
		namedFile{"./README.md", []byte("readme")},
		namedFile{"./" + skillsName, []byte(skillsDoc)},
		namedFile{"./" + binName, content},
	)
}

// tarGzBinaryOnly 模拟不含 skills.md 的归档（裁剪过的旧发布）。
func tarGzBinaryOnly(t *testing.T, binName string, content []byte) []byte {
	t.Helper()
	return tarGzFiles(t, namedFile{"./" + binName, content})
}

func zipOf(t *testing.T, binName string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// Windows 归档同样带 skills.md（build-release.sh 用的是同一份文件列表）
	for _, f := range []namedFile{{skillsName, []byte(skillsDoc)}, {binName, content}} {
		w, _ := zw.Create(f.name)
		w.Write(f.data)
	}
	zw.Close()
	return buf.Bytes()
}

type rel struct {
	tag        string
	prerelease bool
	draft      bool
	assets     map[string][]byte // 资产名 -> 内容
	name       string            // 发布标题（dev 发布用它放构建号）
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
		return map[string]any{"tag_name": r.tag, "name": r.name, "prerelease": r.prerelease, "draft": r.draft,
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
	name := u.assetName(v.String())
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

// GitHub 的 Releases 列表是按 **tag 名**排的，不是按时间：真实返回顺序是
// 9, 8, …, 2, 10, 1（preview.10 排在 preview.1 前面、preview.2 后面）。
// 选"最新"绝不能依赖列表顺序——这里就按 GitHub 的真实顺序喂进去，断言仍然选中 .10。
// 线上验证过：v0.2.0-preview.9 的二进制 `--update --pre` 报的是 latest=0.2.0-preview.10。
func TestLatestIgnoresReleaseListOrder(t *testing.T) {
	var rels []rel
	order := []int{9, 8, 7, 6, 5, 4, 3, 2, 10, 1} // GitHub 的字典序，不是时间序
	for _, n := range order {
		rels = append(rels, release(t, fmt.Sprintf("v1.0.0-preview.%d", n), "linux", fmt.Sprintf("P%d", n)))
	}
	rels = append(rels, release(t, "v0.9.0", "linux", "OLD-STABLE"))
	u := fakeGitHub(t, rels)
	u.Current = "1.0.0-preview.9"

	res, err := u.CheckChannel(context.Background(), ChannelPreview)
	if err != nil {
		t.Fatal(err)
	}
	if res.Latest != "1.0.0-preview.10" || !res.UpdateAvailable {
		t.Fatalf("列表顺序不该影响选版：%+v", res)
	}

	u.Exe, u.Verify = installedExe(t, "OLD"), nil
	up, err := u.Upgrade(context.Background(), UpgradeOptions{Pre: true})
	if err != nil {
		t.Fatal(err)
	}
	if !up.Upgraded || up.To != "1.0.0-preview.10" || read(t, u.Exe) != "P10" {
		t.Fatalf("升级应落到 preview.10：%+v %q", up, read(t, u.Exe))
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
	// 升级会解析符号链接/短路径（macOS /var→/private/var，Windows 8.3 短名），比较解析后的路径
	wantPath, _ := filepath.EvalSymlinks(u.Exe)
	if !res.Upgraded || res.From != "1.0.0" || res.To != "1.1.0" || res.Path != wantPath {
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

// dropConn 不返回响应、直接断开连接，客户端会看到 EOF —— 这正是用户升级时遇到的失败形态。
func dropConn(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return
	}
	if conn, _, err := hj.Hijack(); err == nil {
		_ = conn.Close()
	}
}

func TestBackoffIsExponential(t *testing.T) {
	u := &Updater{}
	for _, c := range []struct {
		n    int
		want time.Duration
	}{{1, 500 * time.Millisecond}, {2, time.Second}, {3, 2 * time.Second}, {4, 4 * time.Second}, {5, 8 * time.Second}, {6, maxRetryDelay}, {7, maxRetryDelay}} {
		if got := u.backoff(c.n); got != c.want {
			t.Fatalf("backoff(%d) = %s, want %s", c.n, got, c.want)
		}
	}
	u2 := &Updater{RetryDelay: 100 * time.Millisecond}
	if got := u2.backoff(1); got != 100*time.Millisecond {
		t.Fatalf("自定义 base: %s", got)
	}
	if got := u2.backoff(3); got != 400*time.Millisecond {
		t.Fatalf("自定义 base 指数: %s", got)
	}
}

func TestCheckRetriesTransientFailuresThenSucceeds(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch atomic.AddInt32(&hits, 1) {
		case 1:
			dropConn(w) // EOF
		case 2:
			w.WriteHeader(http.StatusBadGateway) // 502
		default:
			_, _ = w.Write([]byte(`[{"tag_name":"v9.9.9","assets":[]}]`))
		}
	}))
	defer srv.Close()

	var logBuf bytes.Buffer
	u := &Updater{Repo: "o/r", APIBase: srv.URL, Client: srv.Client(), AllowHTTP: true,
		Progress: &logBuf, RetryDelay: time.Millisecond}
	res, err := u.Check(context.Background(), false)
	if err != nil {
		t.Fatalf("瞬断应重试后成功：%v", err)
	}
	if res.Latest != "9.9.9" { // Check 返回的 latest 已去掉 v 前缀
		t.Fatalf("latest = %q", res.Latest)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("请求次数 = %d, want 3", got)
	}
	logs := logBuf.String()
	if !strings.Contains(logs, "后重试") || !strings.Contains(logs, "第 2/3 次") {
		t.Fatalf("重试日志缺失：%q", logs)
	}
}

func TestFetchPersistentFailureExplainsManualDownload(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		dropConn(w)
	}))
	defer srv.Close()

	u := &Updater{Repo: "o/r", APIBase: srv.URL, Client: srv.Client(), AllowHTTP: true,
		Progress: io.Discard, RetryDelay: time.Millisecond}
	_, err := u.Check(context.Background(), false)
	var te *types.Error
	if !errors.As(err, &te) {
		t.Fatalf("want types.Error: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("请求次数 = %d, want 3（默认 3 次尝试）", got)
	}
	if !strings.Contains(te.Message, "已尝试 3 次") {
		t.Fatalf("message 未说明重试次数：%q", te.Message)
	}
	if !strings.Contains(te.Message, "request failed") {
		t.Fatalf("message 应保留 request failed 前缀：%q", te.Message)
	}
	if !strings.Contains(te.Hint, "检查网络/代理") {
		t.Fatalf("hint 应提示网络/镜像：%q", te.Hint)
	}
	if !strings.Contains(te.Hint, "https://github.com/o/r/releases") {
		t.Fatalf("hint 未给出手动下载入口：%q", te.Hint)
	}
}

func TestFetchDoesNotRetryPermanentFailures(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	u := &Updater{Repo: "o/r", APIBase: srv.URL, Client: srv.Client(), AllowHTTP: true,
		Progress: io.Discard, RetryDelay: time.Millisecond}
	_, err := u.Check(context.Background(), false)
	var te *types.Error
	if !errors.As(err, &te) || te.Code != types.CodeNotFound {
		t.Fatalf("want not found: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("404 不应重试，请求次数 = %d", got)
	}
	if strings.Contains(te.Message, "已尝试") {
		t.Fatalf("未重试却写了尝试次数：%q", te.Message)
	}
}

func TestRetryableNetClassification(t *testing.T) {
	if retryableNet(context.Canceled) || retryableNet(context.DeadlineExceeded) {
		t.Fatal("取消/超时不该重试")
	}
	for _, msg := range []string{"unexpected EOF", "read tcp: connection reset by peer", "broken pipe", "i/o timeout", "TLS handshake timeout"} {
		if !retryableNet(errors.New(msg)) {
			t.Fatalf("%q 应可重试", msg)
		}
	}
	for _, msg := range []string{"unknown authority", "no such file or directory", "release not found"} {
		if retryableNet(errors.New(msg)) {
			t.Fatalf("%q 不该重试", msg)
		}
	}
}

// devRelease 构造滚动 dev 发布：固定 tag `dev`、资产名固定（可覆盖上传）、
// 构建号放在 dev-build.txt（权威）与发布标题里。
func devRelease(t *testing.T, buildID string, content string) rel {
	t.Helper()
	arch := tarGz(t, "dtool", []byte(content))
	sum := sha256.Sum256(arch)
	name := "dtool_dev_linux_amd64.tar.gz"
	return rel{tag: "dev", name: buildID, prerelease: true, assets: map[string][]byte{
		name:            arch,
		"dev-build.txt": []byte(buildID + "\n" + strings.Repeat("a", 40) + "\n2026-10-10T00:00:00Z\n"),
		"checksums.txt": []byte(fmt.Sprintf("%s  ./%s\n", hex.EncodeToString(sum[:]), name)),
	}}
}

// dev 渠道：比的是构建身份（不是版本大小），dev-build.txt 里的构建号说了算。
func TestCheckDevChannel(t *testing.T) {
	u := fakeGitHub(t, []rel{release(t, "v1.0.0", "linux", "STABLE"), devRelease(t, "dev-38043572835", "DEV")})
	u.Current, u.OS, u.Arch = "dev-38043572835", "linux", "amd64"

	// 同一个构建（版本串相同）→ 不需要升级
	res, err := u.CheckChannel(context.Background(), ChannelDev)
	if err != nil {
		t.Fatal(err)
	}
	if res.Channel != "dev" || res.Latest != "dev-38043572835" || res.UpdateAvailable {
		t.Fatalf("同一构建不该报可升级：%+v", res)
	}
	if !strings.Contains(res.Hint, "最新 dev 构建") {
		t.Fatalf("提示要说明已是最新：%q", res.Hint)
	}

	// 旧构建：本地是 dev-100，发布里是 dev-38043572835
	u.Current = "dev-100"
	res, err = u.CheckChannel(context.Background(), ChannelDev)
	if err != nil {
		t.Fatal(err)
	}
	if !res.UpdateAvailable {
		t.Fatalf("旧 dev 构建应报可升级：%+v", res)
	}
	if !strings.Contains(res.Hint, "--channel dev") {
		t.Fatalf("提示应给出渠道参数：%q", res.Hint)
	}

	// 只靠 build_id 也能认出来（本地版本串被抹掉的情形，例如从 dev 发布装的二进制）
	u.Current, u.BuildID = "", "38043572835"
	res, err = u.CheckChannel(context.Background(), ChannelDev)
	if err != nil || res.UpdateAvailable {
		t.Fatalf("build_id 相同应视为同一构建：%+v %v", res, err)
	}
}

// dev 渠道升级：资产名固定（dtool_dev_<os>_<arch>），自检用构建号。
func TestUpgradeDevChannel(t *testing.T) {
	u := fakeGitHub(t, []rel{devRelease(t, "dev-38043572835", "DEV-BINARY")})
	u.Current, u.OS, u.Arch = "dev-100", "linux", "amd64"
	exe := installedExe(t, "OLD")
	u.Exe, u.Verify = exe, nil

	res, err := u.Upgrade(context.Background(), UpgradeOptions{Channel: ChannelDev})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Upgraded || res.To != "dev-38043572835" {
		t.Fatalf("应升级到 dev 构建：%+v", res)
	}
	if got := read(t, exe); got != "DEV-BINARY" {
		t.Fatalf("二进制未替换：%q", got)
	}

	// 再跑一次：同一构建 → 不升级（幂等）。真实场景里这是「装完后重启再跑」，
	// 所以把 Updater 看到的当前版本同步成新装的构建号。
	u.Current = "dev-38043572835"
	res, err = u.Upgrade(context.Background(), UpgradeOptions{Channel: ChannelDev})
	if err != nil || res.Upgraded {
		t.Fatalf("同一构建不该重复升级：%+v %v", res, err)
	}
}

// dev 发布不能污染 stable / preview 渠道：它的 tag 不是版本号，选版时会自然跳过。
func TestDevReleaseDoesNotLeakIntoOtherChannels(t *testing.T) {
	u := fakeGitHub(t, []rel{
		release(t, "v1.0.0", "linux", "STABLE"),
		release(t, "v1.1.0-preview.1", "linux", "PREVIEW"),
		devRelease(t, "dev-38043572835", "DEV"),
	})
	u.Current = "1.0.0"

	res, err := u.CheckChannel(context.Background(), ChannelPreview)
	if err != nil {
		t.Fatal(err)
	}
	if res.Latest != "1.1.0-preview.1" {
		t.Fatalf("preview 渠道不该看到 dev 发布：%+v", res)
	}
	res, err = u.CheckChannel(context.Background(), ChannelStable)
	if err != nil || res.Latest != "1.0.0" {
		t.Fatalf("stable 渠道应只看正式版：%+v %v", res, err)
	}
}

// 没有 dev 发布时要给出可读的错误与换渠道建议。
func TestDevChannelWithoutRelease(t *testing.T) {
	u := fakeGitHub(t, []rel{release(t, "v1.0.0", "linux", "STABLE")})
	u.Current = "dev-1"
	_, err := u.CheckChannel(context.Background(), ChannelDev)
	var te *types.Error
	if !errors.As(err, &te) || te.Code != types.CodeNotFound {
		t.Fatalf("应报 not found：%v", err)
	}
	if !strings.Contains(te.Hint, "--channel preview") {
		t.Fatalf("提示应给出换渠道的出口：%q", te.Hint)
	}
}

// `--version dev-<id>`：只认滚动发布里那一个构建，别的 id 直接拒绝（避免用户以为能装历史构建）。
func TestUpgradeExplicitDevBuildID(t *testing.T) {
	u := fakeGitHub(t, []rel{devRelease(t, "dev-38043572835", "DEV")})
	u.Current, u.OS, u.Arch = "dev-1", "linux", "amd64"
	u.Exe, u.Verify = installedExe(t, "OLD"), nil

	if _, err := u.Upgrade(context.Background(), UpgradeOptions{Version: "dev-999"}); err == nil {
		t.Fatal("滚动发布里没有的 dev 构建应报错")
	}
	res, err := u.Upgrade(context.Background(), UpgradeOptions{Version: "dev-38043572835"})
	if err != nil || !res.Upgraded {
		t.Fatalf("指定当前滚动构建应能升级：%+v %v", res, err)
	}
}

// --skills：把该版本的 skills.md 一并取出来（从**已校验**的归档里取，不额外走网络）。
func TestUpgradeWritesSkills(t *testing.T) {
	u := fakeGitHub(t, []rel{release(t, "v1.1.0", "linux", "NEW")})
	u.Current, u.Exe = "1.0.0", installedExe(t, "OLD")
	u.Verify = nil

	// 裸 --skills（NoOptDefVal="."）→ 当前目录
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)

	res, err := u.Upgrade(context.Background(), UpgradeOptions{SkillsPath: "."})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Upgraded || !res.SkillsChanged || !filepath.IsAbs(res.SkillsPath) {
		t.Fatalf("res = %+v", res)
	}
	if read(t, filepath.Join(dir, "skills.md")) != skillsDoc {
		t.Fatalf("没写到当前目录：%s = %q", res.SkillsPath, read(t, res.SkillsPath))
	}
	if !strings.Contains(res.Message, "skills.md 已更新") {
		t.Fatalf("message = %q", res.Message)
	}

	// 已经是最新版本：不动二进制，手册仍按请求写出；内容一样 → changed=false（脚本可据它决定要不要重载）
	u.Current = "1.1.0"
	u.Verify = func(string, string) error { t.Fatal("must not install"); return nil }
	res, err = u.Upgrade(context.Background(), UpgradeOptions{SkillsPath: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.Upgraded || res.SkillsChanged || !strings.Contains(res.Message, "无需升级") {
		t.Fatalf("res = %+v", res)
	}
	if !strings.Contains(res.Message, "内容无变化") {
		t.Fatalf("message = %q", res.Message)
	}

	// 本地手册被改过（或版本较旧）→ 应当重新写回并标 changed
	if err := os.WriteFile(filepath.Join(dir, "skills.md"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = u.Upgrade(context.Background(), UpgradeOptions{SkillsPath: dir})
	if err != nil || !res.SkillsChanged {
		t.Fatalf("res = %+v %v", res, err)
	}
	if read(t, filepath.Join(dir, "skills.md")) != skillsDoc {
		t.Fatal("没有覆盖旧的 skills.md")
	}
}

// --skills 的取值规则：目录（已存在 / 带斜杠 / 无 .md 后缀）补 skills.md，明确的 .md 当文件。
func TestSkillsTargetRules(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct{ in, want string }{
		{dir, filepath.Join(dir, "skills.md")},
		{dir + "/", filepath.Join(dir, "skills.md")},
		{filepath.Join(dir, "docs"), filepath.Join(dir, "docs", "skills.md")},
		{filepath.Join(dir, "docs", "agent.md"), filepath.Join(dir, "docs", "agent.md")},
	} {
		got, err := skillsTarget(c.in)
		if err != nil || got != c.want {
			t.Errorf("skillsTarget(%q) = %q, %v；期望 %q", c.in, got, err, c.want)
		}
	}
	if _, err := skillsTarget("   "); err == nil {
		t.Error("空路径应报用法错误")
	}
	if st, err := os.Stat(filepath.Join(dir, "docs")); err != nil || !st.IsDir() {
		t.Fatalf("无 .md 后缀的路径应被创建为目录：%v", err)
	}
}

// 归档里没有 skills.md（裁剪过的旧发布）：报可读错误，且**不动二进制**（先写手册、后换二进制）。
func TestUpgradeSkillsMissingInArchive(t *testing.T) {
	u := fakeGitHub(t, []rel{release(t, "v1.1.0", "linux", "NEW", func(r *rel) {
		for n := range r.assets {
			if strings.HasPrefix(n, "dtool_") {
				r.assets[n] = tarGzBinaryOnly(t, "dtool", []byte("NEW"))
				sum := sha256.Sum256(r.assets[n])
				r.assets["checksums.txt"] = []byte(hex.EncodeToString(sum[:]) + "  ./" + n + "\n")
			}
		}
	})})
	u.Current, u.Exe = "1.0.0", installedExe(t, "OLD")
	u.Verify = func(string, string) error { t.Fatal("must not install"); return nil }

	if _, err := u.Upgrade(context.Background(), UpgradeOptions{SkillsPath: t.TempDir()}); err == nil {
		t.Fatal("归档缺 skills.md 时应报错")
	}
	if read(t, u.Exe) != "OLD" {
		t.Fatal("binary modified")
	}
	if entries, _ := os.ReadDir(filepath.Dir(u.Exe)); len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
}

// Windows 走 zip，skills.md 同样要能取出来。
func TestUpgradeSkillsFromZip(t *testing.T) {
	u := fakeGitHub(t, []rel{release(t, "v1.1.0", "windows", "NEW-EXE")})
	u.OS = "windows"
	dir := t.TempDir()
	u.Current, u.Exe = "1.0.0", filepath.Join(dir, "dtool.exe")
	if err := os.WriteFile(u.Exe, []byte("OLD-EXE"), 0o755); err != nil {
		t.Fatal(err)
	}
	u.Verify = nil
	if _, err := u.Upgrade(context.Background(), UpgradeOptions{SkillsPath: filepath.Join(dir, "docs")}); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(dir, "docs", "skills.md")) != skillsDoc {
		t.Fatal("zip 归档里的 skills.md 没被取出")
	}
}
