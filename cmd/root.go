package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/dezhishen/dtool/internal/action"
	"github.com/dezhishen/dtool/internal/buildinfo"
	"github.com/dezhishen/dtool/internal/config"
	"github.com/dezhishen/dtool/internal/pipeline"
	"github.com/dezhishen/dtool/internal/workspace"
	"github.com/dezhishen/dtool/pkg/types"
	"github.com/spf13/cobra"
)

type globalFlags struct {
	config      string
	workspace   string
	tags        string
	notes       string
	from        string
	previewRows int
	noRecord    bool
}

var g globalFlags

// cfgFont 来自配置文件的字体路径；--font 优先。
var cfgFont string

// Execute 运行根命令；失败时在 stdout 输出 ErrorResponse JSON，人类可读信息写 stderr。
func Execute(version string) error {
	info := buildinfo.Get()
	info.Version, info.Channel = version, buildinfo.Channel(version)
	root := &cobra.Command{
		Use:               "dtool",
		Short:             "本地数据 Pipeline CLI：Excel → JSON → SQL → 图表，全程记录为 Action",
		Version:           version,
		SilenceUsage:      true,
		SilenceErrors:     true,
		PersistentPreRunE: loadConfig,
	}
	pf := root.PersistentFlags()
	pf.StringVarP(&g.config, "config", "c", "", "配置文件（YAML：font / workspace / preview_rows）")
	pf.StringVar(&g.workspace, "workspace", ".dtool", "工作区目录")
	pf.StringVar(&g.tags, "tags", "", "逗号分隔的标签，写入 Action metadata")
	pf.StringVar(&g.notes, "notes", "", "备注，写入 Action metadata")
	pf.StringVar(&g.from, "from", "", "派生来源：action:<id> 或 latest:<type>（仅记录血缘）")
	pf.IntVar(&g.previewRows, "preview-rows", 20, "Action 预览行数")
	pf.BoolVar(&g.noRecord, "no-record", false, "跳过 Action 记录")

	var checkUpdate, pre bool
	root.Flags().BoolVar(&checkUpdate, "update", false, "检查是否有新版本（不安装；配合 --pre 包含预览版）")
	root.Flags().BoolVar(&pre, "pre", false, "与 --update 配合：包含预览版")
	root.RunE = func(c *cobra.Command, _ []string) error {
		if pre && !checkUpdate {
			return types.Errorf(types.CodeUsage, "--pre 需与 --update 一起使用")
		}
		if checkUpdate {
			return runCheckUpdate(c, version, pre)
		}
		return c.Help()
	}

	root.SetVersionTemplate("dtool version " + info.Summary() + "\n")
	root.AddCommand(newVersionCmd(info), newUpgradeCmd(version), newConvertCmd(), newQueryCmd(), newVisualizeCmd(), newPipelineCmd(), newActionsCmd(), newDatasetsCmd())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := root.ExecuteContext(ctx)
	if err == nil {
		return nil
	}
	te := types.AsError(err, types.CodeUsage)
	_ = printJSON(os.Stdout, te.Response())
	fmt.Fprintln(os.Stderr, "error:", te.Message)
	return te
}

func printJSON(w interface{ Write([]byte) (int, error) }, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func openWorkspace() (*workspace.Workspace, error) {
	ws, err := workspace.Open(g.workspace)
	if err != nil {
		return nil, types.Errorf(types.CodeGeneral, "open workspace: %v", err)
	}
	return ws, nil
}

func newEnv(ctx context.Context) (*pipeline.Env, error) {
	ws, err := openWorkspace()
	if err != nil {
		return nil, err
	}
	var tags []string
	for _, t := range strings.Split(g.tags, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	return &pipeline.Env{
		Ctx: ctx, WS: ws, Rec: &action.Recorder{WS: ws},
		Meta:    action.Metadata{Tags: tags, Notes: g.notes},
		Preview: g.previewRows, NoRecord: g.noRecord, Command: commandLine(),
	}, nil
}

func commandLine() string {
	parts := make([]string, len(os.Args))
	for i, a := range os.Args {
		if strings.ContainsAny(a, " \t\"'$`") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		parts[i] = a
	}
	parts[0] = "dtool"
	return strings.Join(parts, " ")
}

func requireFlags(c *cobra.Command, names ...string) error {
	for _, n := range names {
		if !c.Flags().Changed(n) {
			return types.Errorf(types.CodeUsage, "missing required flag --%s", n)
		}
	}
	return nil
}

func loadConfig(c *cobra.Command, _ []string) error {
	cfgFont = ""
	if g.config == "" {
		return nil
	}
	cfg, err := config.Load(g.config)
	if err != nil {
		return err
	}
	cfgFont = cfg.Font
	if cfg.Workspace != "" && !c.Flags().Changed("workspace") {
		g.workspace = cfg.Workspace
	}
	if cfg.PreviewRows > 0 && !c.Flags().Changed("preview-rows") {
		g.previewRows = cfg.PreviewRows
	}
	return nil
}

// applyFont 在未显式传 --font 时使用配置文件中的字体。
func applyFont(c *cobra.Command, font *string) {
	if !c.Flags().Changed("font") && cfgFont != "" {
		*font = cfgFont
	}
}
