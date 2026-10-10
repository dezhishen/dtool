package cmd

import (
	"strings"

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
	var target, channel, skills string
	var pre bool
	c := &cobra.Command{
		Use:   "upgrade",
		Short: "升级 dtool 到最新版或指定版本（校验 sha256，Windows 下安全替换运行中的文件）",
		Long: `按渠道升级：

  stable   最新正式版（默认）
  preview  正式版 + 预览版（vX.Y.0-preview.N），等价于旧的 --pre
  dev      main 的最新构建（滚动发布 tag "dev"，比的是构建号而不是版本大小）

` + "`--version`" + ` 可指定任意历史版本（可降级）；dev 构建写 ` + "`--version dev`" + ` 或
` + "`--version dev-<run id>`" + `，但 dev 渠道只保留最新一次构建。

` + "`--skills[=路径]`" + ` 顺带把**这个版本**的 SKILL.md（Agent 技能手册）另存一份：

  --skills              写到当前目录（./SKILL.md）
  --skills=docs/        写到 docs/SKILL.md（目录不存在则创建）
  --skills=agent.md     写到指定文件

不带 ` + "`--skills`" + ` 就完全不动文件。已经是最新版本时也能单独取手册（只下载校验归档，
不碰二进制）。取值要用等号写，` + "`--skills docs/`" + ` 这种写法会报用法错误。`,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 0 {
				// --skills 是可选值开关（裸 --skills 表示当前目录），pflag 只认 `=` 形式的取值：
				// 写成 `--skills docs/` 会把 docs/ 留成位置参数，这里必须给出可操作的提示，
				// 否则用户只会看到一句「未知命令」。
				return types.Errorf(types.CodeUsage, "意外的位置参数：%s", strings.Join(args, " ")).
					WithHint("--skills 的取值要用等号写：--skills=目标目录；裸 --skills 表示写到当前目录")
			}
			return nil
		},
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
				updater.UpgradeOptions{Version: target, Channel: ch, SkillsPath: skills})
			if err != nil {
				return err
			}
			return printJSON(c.OutOrStdout(), res)
		},
	}
	c.Flags().StringVar(&target, "version", "", "目标版本，如 v1.2.0、1.2.0-preview.1，或 dev / dev-<run id>（可降级）")
	c.Flags().StringVar(&channel, "channel", "", "升级渠道：stable（默认）/ preview / dev")
	c.Flags().BoolVar(&pre, "pre", false, "等价于 --channel preview")
	c.Flags().StringVar(&skills, "skills", "",
		"顺带另存该版本的 SKILL.md：--skills=目录（写 目录/SKILL.md）/ --skills=文件.md；裸 --skills 写到当前目录")
	c.Flags().Lookup("skills").NoOptDefVal = "."
	return c
}
