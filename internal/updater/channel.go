package updater

import (
	"context"
	"errors"
	"strings"

	"github.com/dezhishen/dtool/pkg/types"
)

// 升级渠道。三个渠道对应三种「最新版」的定义：
//
//	stable  最新正式版（含补丁版），不带 preview 的 tag
//	preview 正式版 + 预览版（vX.Y.0-preview.N）
//	dev     **滚动** dev 发布（固定 tag `dev`，每次 main 构建覆盖其资产）
//
// dev 渠道是滚动的原因：dev 构建是「main 的最新状态」，按次建 tag 会堆出成百上千个
// 发布，还得额外做清理策略；固定一个 tag、每次 `--clobber` 替换资产，语义就是
// 「当前 main 的构建」，也正好和 `dtool upgrade --channel dev` 的预期一致。
type Channel string

const (
	ChannelStable  Channel = "stable"
	ChannelPreview Channel = "preview"
	ChannelDev     Channel = "dev"
)

// devAssetVersion 是 dev 渠道资产名里使用的版本：资产名固定为
// `dtool_dev_<os>_<arch>.tar.gz`，这样每次构建都能覆盖同名文件（见 CI 的 dev-build）。
// 二进制里嵌的版本仍是 `dev-<run id>`（可由 dev-build.txt 读到），两者刻意分开：
// 资产名要稳定，版本号要能区分构建。
const devAssetVersion = "dev"

// ParseChannel 解析 --channel；空串按 stable。
func ParseChannel(s string) (Channel, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(ChannelStable):
		return ChannelStable, nil
	case string(ChannelPreview):
		return ChannelPreview, nil
	case string(ChannelDev):
		return ChannelDev, nil
	}
	return "", types.Errorf(types.CodeUsage, "invalid channel %q", s).
		WithHint("可选：stable（最新正式版）/ preview（含预览版）/ dev（main 的最新构建）")
}

// pre 返回该渠道是否包含预览版（stable/preview 走版本比较时用）。
func (c Channel) pre() bool { return c == ChannelPreview }

// devTag 返回滚动 dev 发布的 tag（可用 DTOOL_DEV_TAG 指向镜像上的同名发布）。
func (u *Updater) devTag() string {
	if u.DevTag != "" {
		return u.DevTag
	}
	return defaultDevTag
}

// IsDevVersion 判断一个版本串是否属于 dev 渠道（`dev` 或 `dev-<run id>`）。
func IsDevVersion(s string) bool {
	v := strings.TrimSpace(s)
	return v == devAssetVersion || strings.HasPrefix(v, devAssetVersion+"-")
}

// devRelease 取滚动 dev 发布，并解析它的构建号（如 `dev-38043572835`）。
// 构建号优先取 `dev-build.txt`（权威，镜像也能带），退回发布标题，最后退回 tag。
func (u *Updater) devRelease(ctx context.Context) (*ghRelease, string, error) {
	rel := &ghRelease{}
	if err := u.apiJSON(ctx, u.tagPath(u.devTag()), rel); err != nil {
		var te *types.Error
		if errors.As(err, &te) && te.Code == types.CodeNotFound {
			return nil, "", types.Errorf(types.CodeNotFound, "没有 dev 发布（tag %s）", u.devTag()).
				WithHint("dev 渠道需要 main 上有构建；用 --channel preview 或 --channel stable 换渠道")
		}
		return nil, "", err
	}
	return rel, u.devBuildID(ctx, rel), nil
}

// devBuildID 读 dev 发布的构建号。`dev-build.txt` 第一行即构建号（形如 dev-38043572835），
// 之所以要这个文件而不是靠资产名：资产名必须稳定（覆盖上传），版本号则必须能区分构建。
func (u *Updater) devBuildID(ctx context.Context, rel *ghRelease) string {
	if a := findAsset(rel, devBuildAsset); a != nil {
		if body, err := u.download(ctx, a, 64<<10); err == nil {
			for _, line := range strings.Split(string(body), "\n") {
				if line = strings.TrimSpace(line); line != "" {
					return line
				}
			}
		} else {
			u.logf("读取 %s 失败（%v），退回发布标题", devBuildAsset, err)
		}
	}
	if n := strings.TrimSpace(rel.Name); n != "" {
		return n
	}
	return rel.TagName
}

// target 是选定的升级目标。
type target struct {
	rel     *ghRelease
	assetV  string // 资产名里用的版本（dev 渠道固定为 "dev"）
	expect  string // 展示与自检用的版本串
	buildID string // dev 渠道的构建号，形如 dev-38043572835
	channel Channel
}

// sameBuild 判断「当前运行的这个二进制就是这个目标」。
//
// dev 渠道不能比大小（`dev-<run id>` 之间没有版本序），只能比身份：
// 二进制里的版本串或 build_id 与目标构建号一致，就算已经是最新。
func (u *Updater) sameBuild(t target) bool {
	if t.channel != ChannelDev || t.buildID == "" {
		return false
	}
	if u.Current == t.buildID {
		return true
	}
	if u.BuildID != "" && u.BuildID == strings.TrimPrefix(t.buildID, devAssetVersion+"-") {
		return true
	}
	return false
}

// resolve 把「渠道 + 显式版本」解析成升级目标。
func (u *Updater) resolve(ctx context.Context, ch Channel, want string) (target, error) {
	if want != "" && IsDevVersion(want) {
		// `--version dev` / `--version dev-<id>` 等价于走 dev 渠道；具体 id 只用于校验
		rel, id, err := u.devRelease(ctx)
		if err != nil {
			return target{}, err
		}
		if want != devAssetVersion && want != id {
			return target{}, types.Errorf(types.CodeNotFound, "指定的 dev 构建 %s 不在滚动发布里（当前是 %s）", want, id).
				WithHint("dev 渠道是滚动的，只保留最新一次 main 构建；直接 `dtool upgrade --channel dev` 即可")
		}
		return target{rel: rel, assetV: devAssetVersion, expect: id, buildID: id, channel: ChannelDev}, nil
	}
	if want != "" {
		v, err := ParseVersion(want)
		if err != nil {
			return target{}, types.Errorf(types.CodeUsage, "%v", err)
		}
		rel := &ghRelease{}
		if err := u.apiJSON(ctx, u.tagPath(v.Tag()), rel); err != nil {
			return target{}, err
		}
		rel.version = v
		return target{rel: rel, assetV: v.String(), expect: v.String(), channel: ChannelStable}, nil
	}
	if ch == ChannelDev {
		rel, id, err := u.devRelease(ctx)
		if err != nil {
			return target{}, err
		}
		return target{rel: rel, assetV: devAssetVersion, expect: id, buildID: id, channel: ChannelDev}, nil
	}
	rel, err := u.latest(ctx, ch.pre())
	if err != nil {
		return target{}, err
	}
	return target{rel: rel, assetV: rel.version.String(), expect: rel.version.String(), channel: ch}, nil
}
