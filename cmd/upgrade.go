package cmd

import (
	"github.com/dezhishen/dtool/internal/buildinfo"
	"github.com/dezhishen/dtool/internal/updater"
	"github.com/dezhishen/dtool/pkg/types"
	"github.com/spf13/cobra"
)

// resolveChannel 把 --channel / --pre 收敛成一个渠道：
//   - 都不给 → stable
//   - --pre → preview（旧写法，等价于 --channel preview）
//   - 两者同时给且不一致 → 用法错误（避免"看起来生效了其实没生效"）
func resolveChannel(channel string, pre bool) (updater.Channel, error) {
	ch, err := updater.ParseChannel(channel)
	if err != nil {
		return "", err
	}
	if pre {
		if channel != "" && ch != updater.ChannelPreview {
			return "", types.Errorf(types.CodeUsage, "--pre 与 --channel %s 冲突", channel).
				WithHint("--pre 是 --channel preview 的旧写法，二者只留一个")
		}
		ch = updater.ChannelPreview
	}
	return ch, nil
}

// newUpdater 构造 Updater 并带上当前构建身份：dev 渠道靠 build_id 判断「是不是同一个构建」。
func newUpdater(version string) *updater.Updater {
	u := updater.New(version)
	u.BuildID = buildinfo.Get().BuildID
	return u
}

// runCheckUpdate 对应 `dtool --update [--channel X] [--pre]`：只检查，不改动任何文件。
func runCheckUpdate(c *cobra.Command, version, channel string, pre bool) error {
	ch, err := resolveChannel(channel, pre)
	if err != nil {
		return err
	}
	res, err := newUpdater(version).CheckChannel(c.Context(), ch)
	if err != nil {
		return err
	}
	return printJSON(c.OutOrStdout(), res)
}

func newUpgradeCmd(version string) *cobra.Command {
	var target, channel string
	var pre bool
	c := &cobra.Command{
		Use:   "upgrade",
		Short: "升级 dtool 到最新版或指定版本（校验 sha256，Windows 下安全替换运行中的文件）",
		Long: `按渠道升级：

  stable   最新正式版（默认）
  preview  正式版 + 预览版（vX.Y.0-preview.N），等价于旧的 --pre
  dev      main 的最新构建（滚动发布 tag "dev"，比的是构建号而不是版本大小）

` + "`--version`" + ` 可指定任意历史版本（可降级）；dev 构建写 ` + "`--version dev`" + ` 或
` + "`--version dev-<run id>`" + `，但 dev 渠道只保留最新一次构建。`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			ch, err := resolveChannel(channel, pre)
			if err != nil {
				return err
			}
			if target != "" && pre {
				return types.Errorf(types.CodeUsage, "--version 与 --pre 不能同时使用").
					WithHint("预览版请直接指定，如 --version 1.2.0-preview.1")
			}
			if target != "" && channel != "" && !updater.IsDevVersion(target) {
				return types.Errorf(types.CodeUsage, "--version 与 --channel 不能同时使用").
					WithHint("指定具体版本即已选定目标，不必再给渠道")
			}
			res, err := newUpdater(version).Upgrade(c.Context(),
				updater.UpgradeOptions{Version: target, Channel: ch})
			if err != nil {
				return err
			}
			return printJSON(c.OutOrStdout(), res)
		},
	}
	c.Flags().StringVar(&target, "version", "", "目标版本，如 v1.2.0、1.2.0-preview.1，或 dev / dev-<run id>（可降级）")
	c.Flags().StringVar(&channel, "channel", "", "升级渠道：stable（默认）/ preview / dev")
	c.Flags().BoolVar(&pre, "pre", false, "等价于 --channel preview")
	return c
}
