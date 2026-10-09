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
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dezhishen/dtool/pkg/types"
)

const (
	defaultRepo   = "dezhishen/dtool"
	defaultAPI    = "https://api.github.com"
	maxArchive    = 200 << 20
	maxBinary     = 400 << 20
	checksumsName = "checksums.txt"
)

type Updater struct {
	Repo      string
	APIBase   string
	Client    *http.Client
	Current   string // 当前版本，可带 v；非发版构建（如 dev）视为未知
	OS, Arch  string
	Exe       string // 要替换的可执行文件，空则取当前进程
	Token     string
	AllowHTTP bool                             // 仅测试用：允许 http 下载
	Verify    func(path, version string) error // 替换前运行新二进制自检，nil 跳过
	Progress  io.Writer
}

// New 使用默认配置；DTOOL_REPO / DTOOL_UPDATE_API 可覆盖仓库与 API 地址（镜像、测试）。
func New(current string) *Updater {
	u := &Updater{
		Repo: defaultRepo, APIBase: defaultAPI, Current: current,
		Client: &http.Client{Timeout: 5 * time.Minute},
		OS:     runtime.GOOS, Arch: runtime.GOARCH,
		Token:    firstNonEmpty(os.Getenv("GITHUB_TOKEN"), os.Getenv("GH_TOKEN")),
		Verify:   verifyBinary,
		Progress: os.Stderr,
	}
	if v := os.Getenv("DTOOL_REPO"); v != "" {
		u.Repo = v
	}
	if v := os.Getenv("DTOOL_UPDATE_API"); v != "" {
		u.APIBase = strings.TrimRight(v, "/")
	}
	return u
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func (u *Updater) logf(format string, a ...any) {
	if u.Progress != nil {
		fmt.Fprintf(u.Progress, format+"\n", a...)
	}
}

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Prerelease  bool      `json:"prerelease"`
	Draft       bool      `json:"draft"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []ghAsset `json:"assets"`
	version     Version
}

type CheckResult struct {
	Current         string    `json:"current"`
	Latest          string    `json:"latest"`
	UpdateAvailable bool      `json:"update_available"`
	Prerelease      bool      `json:"prerelease"`
	ReleaseURL      string    `json:"release_url"`
	PublishedAt     time.Time `json:"published_at"`
	Hint            string    `json:"hint,omitempty"`
}

type UpgradeOptions struct {
	Version string // 指定版本；空则取最新
	Pre     bool   // 最新版是否包含预览版
}

type UpgradeResult struct {
	Success    bool   `json:"success"`
	Upgraded   bool   `json:"upgraded"`
	From       string `json:"from"`
	To         string `json:"to"`
	Path       string `json:"path,omitempty"`
	ReleaseURL string `json:"release_url,omitempty"`
	Message    string `json:"message"`
}

func (u *Updater) get(ctx context.Context, url string, auth bool) (*http.Response, error) {
	if !u.AllowHTTP && !strings.HasPrefix(url, "https://") {
		return nil, types.Errorf(types.CodeExec, "refusing non-https url: %s", url)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "dtool-updater")
	if auth {
		req.Header.Set("Accept", "application/vnd.github+json")
		if u.Token != "" {
			req.Header.Set("Authorization", "Bearer "+u.Token)
		}
	}
	return u.Client.Do(req)
}

func (u *Updater) apiJSON(ctx context.Context, p string, out any) error {
	resp, err := u.get(ctx, u.APIBase+p, true)
	if err != nil {
		return types.Errorf(types.CodeExec, "request failed: %v", err).WithHint("检查网络，或设置 DTOOL_UPDATE_API 使用镜像")
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return types.Errorf(types.CodeNotFound, "release not found (%s)", p)
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return types.Errorf(types.CodeExec, "GitHub API rate limited (HTTP %d)", resp.StatusCode).
			WithHint("设置环境变量 GITHUB_TOKEN 后重试")
	case resp.StatusCode != http.StatusOK:
		return types.Errorf(types.CodeExec, "unexpected HTTP %d from %s", resp.StatusCode, p)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
}

func (u *Updater) tagPath(tag string) string {
	return "/repos/" + u.Repo + "/releases/tags/" + tag
}

// latest 返回最新发布：pre=false 只看正式/补丁版；pre=true 同时考虑预览版（同号正式版仍高于预览版）。
func (u *Updater) latest(ctx context.Context, pre bool) (*ghRelease, error) {
	var list []ghRelease
	if err := u.apiJSON(ctx, "/repos/"+u.Repo+"/releases?per_page=100", &list); err != nil {
		return nil, err
	}
	var best *ghRelease
	for i := range list {
		r := &list[i]
		v, err := ParseVersion(r.TagName)
		if err != nil || r.Draft || ((r.Prerelease || v.Prerelease()) && !pre) {
			continue
		}
		r.version = v
		if best == nil || v.Compare(best.version) > 0 {
			best = r
		}
	}
	if best == nil {
		return nil, types.Errorf(types.CodeNotFound, "no release found in %s", u.Repo)
	}
	return best, nil
}

func (u *Updater) current() (Version, bool) {
	v, err := ParseVersion(u.Current)
	return v, err == nil
}

// Check 仅检查是否有新版本，不修改任何文件。
func (u *Updater) Check(ctx context.Context, pre bool) (*CheckResult, error) {
	r, err := u.latest(ctx, pre)
	if err != nil {
		return nil, err
	}
	res := &CheckResult{Current: strings.TrimPrefix(u.Current, "v"), Latest: r.version.String(),
		Prerelease: r.version.Prerelease(), ReleaseURL: r.HTMLURL, PublishedAt: r.PublishedAt}
	cur, ok := u.current()
	res.UpdateAvailable = !ok || r.version.Compare(cur) > 0
	switch {
	case !ok:
		res.Hint = "当前为非发版构建，无法比较版本；运行 `dtool upgrade` 升级"
	case res.UpdateAvailable:
		res.Hint = "运行 `dtool upgrade" + map[bool]string{true: " --pre"}[pre] + "` 升级"
	}
	return res, nil
}

func (u *Updater) assetName(v Version) string {
	ext := ".tar.gz"
	if u.OS == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("dtool_%s_%s_%s%s", v.String(), u.OS, u.Arch, ext)
}

func (u *Updater) binName() string {
	if u.OS == "windows" {
		return "dtool.exe"
	}
	return "dtool"
}

func findAsset(r *ghRelease, name string) *ghAsset {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i]
		}
	}
	return nil
}

func (u *Updater) download(ctx context.Context, a *ghAsset, limit int64) ([]byte, error) {
	resp, err := u.get(ctx, a.URL, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, types.Errorf(types.CodeExec, "download %s: HTTP %d", a.Name, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, types.Errorf(types.CodeExec, "download %s exceeds %d bytes", a.Name, limit)
	}
	return data, nil
}

// expectedSum 从 checksums.txt（sha256sum 格式，文件名可带 ./ 或 *）取出 name 的摘要。
func expectedSum(sums []byte, name string) (string, bool) {
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		if strings.TrimPrefix(strings.TrimPrefix(f[1], "*"), "./") == name {
			return strings.ToLower(f[0]), true
		}
	}
	return "", false
}

func extractBinary(archive []byte, name, binName string) ([]byte, error) {
	isBin := func(n string) bool { return path.Base(path.Clean(strings.ReplaceAll(n, "\\", "/"))) == binName }
	if strings.HasSuffix(name, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if f.FileInfo().IsDir() || !isBin(f.Name) {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return readLimited(rc)
		}
		return nil, fmt.Errorf("%s not found in %s", binName, name)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s not found in %s", binName, name)
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && isBin(h.Name) {
			return readLimited(tr)
		}
	}
}

func readLimited(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBinary+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBinary {
		return nil, fmt.Errorf("binary exceeds %d bytes", maxBinary)
	}
	return data, nil
}

func verifyBinary(p, version string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, p, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("new binary failed self-check: %v", err)
	}
	if !strings.Contains(string(out), version) {
		return fmt.Errorf("new binary reports %q, want version %s", strings.TrimSpace(string(out)), version)
	}
	return nil
}

func (u *Updater) exePath() (string, error) {
	exe := u.Exe
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return "", err
		}
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return exe, nil
}

// Upgrade 下载、校验并替换当前可执行文件。
func (u *Updater) Upgrade(ctx context.Context, o UpgradeOptions) (*UpgradeResult, error) {
	var rel *ghRelease
	if o.Version != "" {
		want, err := ParseVersion(o.Version)
		if err != nil {
			return nil, types.Errorf(types.CodeUsage, "%v", err)
		}
		rel = &ghRelease{}
		if err := u.apiJSON(ctx, u.tagPath(want.Tag()), rel); err != nil {
			return nil, err
		}
		rel.version = want
	} else {
		var err error
		if rel, err = u.latest(ctx, o.Pre); err != nil {
			return nil, err
		}
	}

	res := &UpgradeResult{Success: true, From: strings.TrimPrefix(u.Current, "v"), To: rel.version.String(), ReleaseURL: rel.HTMLURL}
	if cur, ok := u.current(); ok {
		if c := rel.version.Compare(cur); c == 0 || (c < 0 && o.Version == "") {
			res.Message = "已是最新版本，无需升级"
			if c < 0 {
				res.Message = "当前版本高于可用的最新版本，无需升级"
			}
			return res, nil
		}
	}

	name := u.assetName(rel.version)
	asset, sums := findAsset(rel, name), findAsset(rel, checksumsName)
	if asset == nil {
		return nil, types.Errorf(types.CodeNotFound, "release %s has no asset for %s/%s", rel.version.Tag(), u.OS, u.Arch).
			WithDetail("expected asset: " + name)
	}
	if sums == nil {
		return nil, types.Errorf(types.CodeExec, "release %s has no %s; refusing to install unverified binary", rel.version.Tag(), checksumsName)
	}

	exe, err := u.exePath()
	if err != nil {
		return nil, err
	}
	u.logf("下载 %s ...", name)
	archive, err := u.download(ctx, asset, maxArchive)
	if err != nil {
		return nil, err
	}
	sumData, err := u.download(ctx, sums, 1<<20)
	if err != nil {
		return nil, err
	}
	want, ok := expectedSum(sumData, name)
	if !ok {
		return nil, types.Errorf(types.CodeExec, "%s has no entry for %s", checksumsName, name)
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return nil, types.Errorf(types.CodeExec, "checksum mismatch for %s", name).
			WithDetail(fmt.Sprintf("expected %s, got %s", want, hex.EncodeToString(got[:])))
	}
	bin, err := extractBinary(archive, name, u.binName())
	if err != nil {
		return nil, types.Errorf(types.CodeExec, "extract %s: %v", name, err)
	}

	// 暂存文件与目标同目录，保证 rename 在同一文件系统内；Windows 上需 .exe 后缀才能自检运行。
	pattern := ".dtool-upgrade-*"
	if u.OS == "windows" {
		pattern += ".exe"
	}
	tmp, err := os.CreateTemp(filepath.Dir(exe), pattern)
	if err != nil {
		return nil, types.Errorf(types.CodeExec, "cannot write to install dir %s: %v", filepath.Dir(exe), err).
			WithHint("需要对安装目录有写权限（Linux/macOS 可用 sudo，Windows 请以管理员运行或换安装位置）")
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	mode := os.FileMode(0o755)
	if st, err := os.Stat(exe); err == nil {
		mode = st.Mode().Perm() | 0o111
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return nil, err
	}
	if u.Verify != nil {
		if err := u.Verify(tmpPath, rel.version.String()); err != nil {
			return nil, types.Errorf(types.CodeExec, "%v", err)
		}
	}

	u.logf("替换 %s", exe)
	if err := replaceExecutable(exe, tmpPath, u.OS == "windows"); err != nil {
		return nil, types.Errorf(types.CodeExec, "replace %s: %v", exe, err).
			WithHint("Windows 下请先关闭其他正在运行的 dtool 进程与占用该文件的程序后重试")
	}
	res.Upgraded, res.Path = true, exe
	res.Message = fmt.Sprintf("已升级到 %s", rel.version.Tag())
	return res, nil
}
