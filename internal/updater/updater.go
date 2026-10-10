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
	"net"
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
	// skillsName：随发布归档一起分发的 agent 手册。它是仓库里那个标准 Agent Skill 包
	// （skills/dtool/SKILL.md）的副本，build-release.sh 把它放进每个平台归档；
	// `upgrade --skills` 直接从**已校验过**的归档里取它，不额外走网络。
	skillsName = "SKILL.md"
	// legacySkillsName：v0.2.0 及以前的归档里这份手册叫 skills.md（当时还没有按
	// Agent Skill 布局组织）。老版本还能装，所以取手册时按新名找、找不到再退回旧名。
	legacySkillsName = "skills.md"
	// defaultDevTag：滚动 dev 发布的 tag（`dtool upgrade --channel dev` 的目标）。
	defaultDevTag = "dev"
	// devBuildAsset：dev 发布里记录构建号的小文件（见 CI 的 dev-build 任务）。
	devBuildAsset = "dev-build.txt"

	// 网络重试：GitHub API 与资产下载经常遇到瞬断（EOF / connection reset / 5xx），
	// 退避按指数增长（base、2×base、4×base…），封顶 maxRetryDelay。
	defaultAttempts   = 3
	defaultRetryDelay = 500 * time.Millisecond
	maxRetryDelay     = 10 * time.Second
)

type Updater struct {
	Repo       string
	APIBase    string
	Client     *http.Client
	Current    string // 当前版本，可带 v；非发版构建（如 dev）视为未知
	BuildID    string // 当前二进制的 CI 运行号（dev 渠道靠它判断「是不是同一个构建」）
	DevTag     string // 滚动 dev 发布的 tag，空取 defaultDevTag（DTOOL_DEV_TAG 可覆盖）
	OS, Arch   string
	Exe        string // 要替换的可执行文件，空则取当前进程
	Token      string
	AllowHTTP  bool                             // 仅测试用：允许 http 下载
	Attempts   int                              // 一个 URL 最多尝试几次（默认 3，含首次）
	RetryDelay time.Duration                    // 首次重试等待，之后指数增长（默认 500ms）
	Verify     func(path, version string) error // 替换前运行新二进制自检，nil 跳过
	Progress   io.Writer
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
	Name        string    `json:"name"`
	Prerelease  bool      `json:"prerelease"`
	Draft       bool      `json:"draft"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []ghAsset `json:"assets"`
	version     Version
}

type CheckResult struct {
	Current         string    `json:"current"`
	Channel         string    `json:"channel"` // stable / preview / dev
	Latest          string    `json:"latest"`
	UpdateAvailable bool      `json:"update_available"`
	Prerelease      bool      `json:"prerelease"`
	ReleaseURL      string    `json:"release_url"`
	PublishedAt     time.Time `json:"published_at"`
	Hint            string    `json:"hint,omitempty"`
}

type UpgradeOptions struct {
	Version string  // 指定版本；空则取最新。dev 渠道可写 dev / dev-<run id>
	Pre     bool    // 兼容旧参数：等价于 Channel=preview
	Channel Channel // stable / preview / dev；空按 Pre 推断，再默认 stable
	// SkillsPath：把目标版本的 SKILL.md 另存到哪里（空 = 不保存）。取值可以是目录
	// （写成 <目录>/SKILL.md，目录不存在则创建）、明确以 .md 结尾的文件路径，
	// 或 "."（当前目录）。见 skillsTarget。
	SkillsPath string
}

type UpgradeResult struct {
	Success    bool   `json:"success"`
	Upgraded   bool   `json:"upgraded"`
	From       string `json:"from"`
	To         string `json:"to"`
	Path       string `json:"path,omitempty"`
	ReleaseURL string `json:"release_url,omitempty"`
	// SkillsPath：SKILL.md 实际写入的位置（绝对路径）；未请求时为 "-" 省略。
	SkillsPath    string `json:"skills_path,omitempty"`
	SkillsChanged bool   `json:"skills_changed,omitempty"`
	Message       string `json:"message"`
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

// attempts 返回一个 URL 最多尝试几次（含首次）。
func (u *Updater) attempts() int {
	if u.Attempts > 1 {
		return u.Attempts
	}
	return defaultAttempts
}

// backoff 返回第 n 次重试前的等待时间：指数退避 base × 2^(n-1)，封顶 maxRetryDelay。
// n 从 1 起（第 1 次重试等 base，第 2 次等 2×base…）。
func (u *Updater) backoff(n int) time.Duration {
	base := u.RetryDelay
	if base <= 0 {
		base = defaultRetryDelay
	}
	if n < 1 {
		n = 1
	}
	if n > 16 {
		n = 16 // 防止移位溢出
	}
	d := base << (n - 1)
	if d <= 0 || d > maxRetryDelay {
		d = maxRetryDelay
	}
	return d
}

// retryableNet 判断一次网络失败是否值得重试（瞬断 vs 明确的拒绝）。
func retryableNet(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{
		"eof", "connection reset", "connection refused", "broken pipe",
		"timeout", "timed out", "temporary failure", "no such host", "tls handshake",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// fetchOnce 取回一个 URL 的响应体；retryable 表示这次失败值得重试。
func (u *Updater) fetchOnce(ctx context.Context, url, what string, auth bool, limit int64) ([]byte, bool, error) {
	resp, err := u.get(ctx, url, auth)
	if err != nil {
		var te *types.Error
		if errors.As(err, &te) { // 非 https 等策略性拒绝：重试没有意义，原样返回
			return nil, false, err
		}
		werr := types.Errorf(types.CodeExec, "request failed: %v", err).
			WithHint("检查网络/代理，或设置 DTOOL_UPDATE_API 使用镜像")
		return nil, retryableNet(err), werr
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if err != nil {
			return nil, retryableNet(err), err // 读一半断了也算瞬断
		}
		if int64(len(body)) > limit {
			return nil, false, types.Errorf(types.CodeExec, "download %s exceeds %d bytes", what, limit)
		}
		return body, false, nil
	case resp.StatusCode == http.StatusNotFound:
		return nil, false, types.Errorf(types.CodeNotFound, "release not found (%s)", what)
	case resp.StatusCode == http.StatusForbidden:
		return nil, false, types.Errorf(types.CodeExec, "GitHub API rate limited (HTTP 403)").
			WithHint("设置环境变量 GITHUB_TOKEN 后重试")
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, true, types.Errorf(types.CodeExec, "GitHub 限流 (HTTP 429)")
	case resp.StatusCode >= 500:
		return nil, true, types.Errorf(types.CodeExec, "GitHub 服务端错误 (HTTP %d)", resp.StatusCode)
	default:
		return nil, false, types.Errorf(types.CodeExec, "unexpected HTTP %d from %s", resp.StatusCode, what)
	}
}

// fetch 带指数退避重试地取回响应体。瞬断（EOF / reset / 5xx / 429）会重试，
// 明确的拒绝（404 / 403 限流 / 非 https / 超限）立即返回。
func (u *Updater) fetch(ctx context.Context, url, what string, auth bool, limit int64) ([]byte, error) {
	attempts, tried := u.attempts(), 0
	var lastErr error
	for i := 1; i <= attempts; i++ {
		tried = i
		body, retryable, err := u.fetchOnce(ctx, url, what, auth, limit)
		if err == nil {
			if i > 1 {
				u.logf("%s：第 %d 次尝试成功", what, i)
			}
			return body, nil
		}
		if ctx.Err() != nil {
			return nil, types.Errorf(types.CodeInterrupted, "已中断：更新请求被取消")
		}
		lastErr = err
		if !retryable || i == attempts {
			break
		}
		delay := u.backoff(i)
		u.logf("请求 %s 失败（%v）；%s 后重试（第 %d/%d 次）", what, err, delay, i+1, attempts)
		select {
		case <-ctx.Done():
			return nil, types.Errorf(types.CodeInterrupted, "已中断：更新请求被取消")
		case <-time.After(delay):
		}
	}
	return nil, u.explain(lastErr, tried)
}

// explain 把最终失败包装成「发生了什么 + 还能怎么办」：网络/代理不通时给出 Releases 页面，
// 让用户不必依赖工具自己去下载。
func (u *Updater) explain(err error, tried int) error {
	te := types.AsError(err, types.CodeExec)
	msg := te.Message
	if tried > 1 {
		msg = fmt.Sprintf("%s（已尝试 %d 次）", msg, tried)
	}
	out := types.Errorf(te.Code, "%s", msg)
	out.Detail = te.Detail
	hint := te.Hint
	if hint != "" {
		hint += "；"
	}
	out.Hint = hint + fmt.Sprintf("可在浏览器直接下载：https://github.com/%s/releases（用 --version 指定版本）", u.Repo)
	return out
}

func (u *Updater) apiJSON(ctx context.Context, p string, out any) error {
	body, err := u.fetch(ctx, u.APIBase+p, p, true, 16<<20)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return types.Errorf(types.CodeExec, "解析 GitHub 响应失败：%v", err).
			WithHint("代理/镜像可能返回了非 GitHub 内容")
	}
	return nil
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

// Check 仅检查是否有新版本，不修改任何文件（pre=true 等价于 --channel preview）。
func (u *Updater) Check(ctx context.Context, pre bool) (*CheckResult, error) {
	ch := ChannelStable
	if pre {
		ch = ChannelPreview
	}
	return u.CheckChannel(ctx, ch)
}

// CheckChannel 按渠道检查更新。dev 渠道比较的是「构建身份」而不是版本大小：
// `dev-<run id>` 之间没有版本序，只能判断目标构建号与当前二进制是否同一个。
func (u *Updater) CheckChannel(ctx context.Context, ch Channel) (*CheckResult, error) {
	t, err := u.resolve(ctx, ch, "")
	if err != nil {
		return nil, err
	}
	res := &CheckResult{Current: strings.TrimPrefix(u.Current, "v"), Channel: string(t.channel),
		Latest: t.expect, Prerelease: t.channel != ChannelStable,
		ReleaseURL: t.rel.HTMLURL, PublishedAt: t.rel.PublishedAt}
	if t.channel == ChannelDev {
		res.UpdateAvailable = !u.sameBuild(t)
		if res.UpdateAvailable {
			res.Hint = "运行 `dtool upgrade --channel dev` 升级到 dev 发布里的最新构建"
		} else {
			res.Hint = "已是 dev 发布里的最新构建"
		}
		return res, nil
	}
	cur, ok := u.current()
	res.UpdateAvailable = !ok || t.rel.version.Compare(cur) > 0
	switch {
	case !ok:
		res.Hint = "当前为非发版构建，无法比较版本；运行 `dtool upgrade` 升级"
	case res.UpdateAvailable:
		res.Hint = "运行 `dtool upgrade" + map[bool]string{true: " --pre"}[ch.pre()] + "` 升级"
	}
	return res, nil
}

// assetName 按版本串拼资产名（dev 渠道传 "dev"：资产名必须稳定，才能覆盖上传）。
func (u *Updater) assetName(version string) string {
	ext := ".tar.gz"
	if u.OS == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("dtool_%s_%s_%s%s", version, u.OS, u.Arch, ext)
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
	return u.fetch(ctx, a.URL, a.Name, false, limit)
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
	return extractFile(archive, name, binName)
}

// extractFile 从归档里取出指定文件（按 basename 匹配，兼容 tar 的 ./ 前缀、目录层级
// 与 Windows 的 \\ 分隔符）。只接受**已校验过 sha256** 的归档内容。
func extractFile(archive []byte, name, want string) ([]byte, error) {
	isWant := func(n string) bool { return path.Base(path.Clean(strings.ReplaceAll(n, "\\", "/"))) == want }
	if strings.HasSuffix(name, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if f.FileInfo().IsDir() || !isWant(f.Name) {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return readLimited(rc)
		}
		return nil, fmt.Errorf("%s not found in %s", want, name)
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
			return nil, fmt.Errorf("%s not found in %s", want, name)
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && isWant(h.Name) {
			return readLimited(tr)
		}
	}
}

// skillsTarget 把 --skills 的取值解析成「要写到哪个文件」。
// 目录（已存在的目录 / 以分隔符结尾 / 没有 .md 后缀）会补上 SKILL.md；"." 就是当前目录。
func skillsTarget(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", types.Errorf(types.CodeUsage, "--skills 需要一个路径").
			WithHint("写 --skills 表示当前目录，或 --skills=<目录|文件.md>")
	}
	isDir := false
	switch {
	case strings.HasSuffix(p, "/") || strings.HasSuffix(p, string(os.PathSeparator)):
		isDir = true
	case !strings.EqualFold(filepath.Ext(p), ".md"):
		// 没有 .md 后缀就当目录：--skills=docs → docs/SKILL.md
		isDir = true
	default:
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			isDir = true
		}
	}
	if !isDir {
		return p, nil
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		return "", types.Errorf(types.CodeExec, "创建目录 %s 失败：%v", p, err).
			WithHint("--skills 的路径要对当前用户可写")
	}
	return filepath.Join(p, skillsName), nil
}

// writeSkills 从归档里取出 SKILL.md 写到 raw 指定的位置，返回绝对路径与内容是否变化。
func (u *Updater) writeSkills(archive []byte, archiveName, raw string) (string, bool, error) {
	dest, err := skillsTarget(raw)
	if err != nil {
		return "", false, err
	}
	// 新名优先；v0.2.0 及以前的归档里叫 skills.md（见 legacySkillsName）。
	data, err := extractFile(archive, archiveName, skillsName)
	if err != nil {
		if data, err = extractFile(archive, archiveName, legacySkillsName); err != nil {
			return "", false, types.Errorf(types.CodeExec, "%s 里既没有 %s 也没有 %s",
				archiveName, skillsName, legacySkillsName).
				WithHint("这份发布的归档不含 agent 手册；从 Releases 页面手动下载即可")
		}
	}
	old, readErr := os.ReadFile(dest)
	changed := readErr != nil || !bytes.Equal(old, data)
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return "", false, types.Errorf(types.CodeExec, "写入 %s 失败：%v", dest, err).
			WithHint("--skills 的路径要对当前用户可写")
	}
	abs, err := filepath.Abs(dest)
	if err != nil {
		abs = dest
	}
	return abs, changed, nil
}

// skillsState 描述手册内容有没有变（幂等脚本据此判断要不要重新加载 agent 手册）。
func skillsState(changed bool) string {
	if changed {
		return "已更新"
	}
	return "内容无变化"
}

// skillsNote 拼出追加到 message 末尾的说明（没取手册时为空串）。单独一个函数是因为
// 正常升级路径要先取手册、最后才组装 message。
func skillsNote(res *UpgradeResult) string {
	if res.SkillsPath == "" {
		return ""
	}
	return fmt.Sprintf("；SKILL.md %s：%s", skillsState(res.SkillsChanged), res.SkillsPath)
}

// attachSkills 记录手册落点并打日志（message 由调用方在最后拼，见 skillsNote）。
func (u *Updater) attachSkills(res *UpgradeResult, path string, changed bool) {
	res.SkillsPath, res.SkillsChanged = path, changed
	u.logf("SKILL.md → %s（%s）", path, skillsState(changed))
}

// fetchVerified 下载发布里当前平台的归档并校验 sha256：拿不到 checksums.txt、
// 缺对应资产或摘要不符，都直接拒绝（不允许装未校验的二进制）。
func (u *Updater) fetchVerified(ctx context.Context, rel *ghRelease, assetV string) (string, []byte, error) {
	name := u.assetName(assetV)
	asset, sums := findAsset(rel, name), findAsset(rel, checksumsName)
	if asset == nil {
		return "", nil, types.Errorf(types.CodeNotFound, "release %s has no asset for %s/%s", rel.TagName, u.OS, u.Arch).
			WithDetail("expected asset: " + name)
	}
	if sums == nil {
		return "", nil, types.Errorf(types.CodeExec, "release %s has no %s; refusing to install unverified binary", rel.TagName, checksumsName)
	}
	u.logf("下载 %s ...", name)
	archive, err := u.download(ctx, asset, maxArchive)
	if err != nil {
		return "", nil, err
	}
	sumData, err := u.download(ctx, sums, 1<<20)
	if err != nil {
		return "", nil, err
	}
	want, ok := expectedSum(sumData, name)
	if !ok {
		return "", nil, types.Errorf(types.CodeExec, "%s has no entry for %s", checksumsName, name)
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return "", nil, types.Errorf(types.CodeExec, "checksum mismatch for %s", name).
			WithDetail(fmt.Sprintf("expected %s, got %s", want, hex.EncodeToString(got[:])))
	}
	return name, archive, nil
}

// skillsOnly 用于「不需要升级」但用户仍要 SKILL.md 的场景：只下载校验归档取出手册，
// 完全不碰二进制（不下载校验的话就没法保证手册与二进制同源）。
func (u *Updater) skillsOnly(ctx context.Context, rel *ghRelease, t target, res *UpgradeResult, o UpgradeOptions) (*UpgradeResult, error) {
	if o.SkillsPath == "" {
		return res, nil
	}
	name, archive, err := u.fetchVerified(ctx, rel, t.assetV)
	if err != nil {
		return nil, err
	}
	path, changed, err := u.writeSkills(archive, name, o.SkillsPath)
	if err != nil {
		return nil, err
	}
	u.attachSkills(res, path, changed)
	res.Message += skillsNote(res)
	return res, nil
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
	ch := o.Channel
	if ch == "" {
		ch = ChannelStable
		if o.Pre {
			ch = ChannelPreview
		}
	}
	if _, err := ParseChannel(string(ch)); err != nil {
		return nil, err
	}
	t, err := u.resolve(ctx, ch, o.Version)
	if err != nil {
		return nil, err
	}
	rel := t.rel

	res := &UpgradeResult{Success: true, From: strings.TrimPrefix(u.Current, "v"), To: t.expect,
		ReleaseURL: rel.HTMLURL}
	if t.channel == ChannelDev {
		if u.sameBuild(t) {
			res.Message = "已是 dev 发布里的最新构建，无需升级"
			return u.skillsOnly(ctx, rel, t, res, o)
		}
	} else if cur, ok := u.current(); ok {
		if c := rel.version.Compare(cur); c == 0 || (c < 0 && o.Version == "") {
			res.Message = "已是最新版本，无需升级"
			if c < 0 {
				res.Message = "当前版本高于可用的最新版本，无需升级"
			}
			return u.skillsOnly(ctx, rel, t, res, o)
		}
	}

	exe, err := u.exePath()
	if err != nil {
		return nil, err
	}
	name, archive, err := u.fetchVerified(ctx, rel, t.assetV)
	if err != nil {
		return nil, err
	}
	bin, err := extractBinary(archive, name, u.binName())
	if err != nil {
		return nil, types.Errorf(types.CodeExec, "extract %s: %v", name, err)
	}
	// SKILL.md 在替换二进制**之前**写：路径不可写时直接失败，不留下
	// 「二进制换了、文档没写」的半成品状态。
	if o.SkillsPath != "" {
		path, changed, err := u.writeSkills(archive, name, o.SkillsPath)
		if err != nil {
			return nil, err
		}
		u.attachSkills(res, path, changed)
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
		// 自检用目标构建号（dev 渠道的二进制里嵌的是 `dev-<run id>`，不是资产名里的 dev）
		if err := u.Verify(tmpPath, t.expect); err != nil {
			return nil, types.Errorf(types.CodeExec, "%v", err)
		}
	}

	u.logf("替换 %s", exe)
	if err := replaceExecutable(exe, tmpPath, u.OS == "windows"); err != nil {
		return nil, types.Errorf(types.CodeExec, "replace %s: %v", exe, err).
			WithHint("Windows 下请先关闭其他正在运行的 dtool 进程与占用该文件的程序后重试")
	}
	res.Upgraded, res.Path = true, exe
	res.Message = fmt.Sprintf("已升级到 %s", t.expect) + skillsNote(res)
	return res, nil
}
