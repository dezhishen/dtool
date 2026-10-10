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
	"github.com/dezhishen/dtool/internal/memguard"
	"github.com/dezhishen/dtool/internal/pipeline"
	"github.com/dezhishen/dtool/internal/query"
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
	maxMemory   string
	store       string
	memPolicy   string
	loadMode    string
}

// parsedMaxMemory 为 --max-memory 的解析结果：nil 表示自动探测。
var parsedMaxMemory *uint64

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
	pf.StringVar(&g.maxMemory, "max-memory", "", "内存预算（如 1.5G）；默认自动探测 cgroup/系统可用内存，0 表示关闭检查")
	pf.StringVar(&g.loadMode, "load-mode", "auto", "JSON 装入方式：auto 按内存预算选执行档（快到省）/ stream 流式（省内存）/ full 整块解析（快，要求更多余量）")
	pf.StringVar(&g.store, "store", "auto", "SQLite 库落在哪：auto 按内存预算自适应 / memory 内存库（快）/ disk 磁盘库（峰值低，硬上限下更稳）")
	pf.StringVar(&g.memPolicy, "mem-policy", "try", "所有档都预计超预算时：try 仍试最省档（失败记入 Action）/ strict 直接失败")

	var checkUpdate, pre bool
	var channel string
	root.Flags().BoolVar(&checkUpdate, "update", false, "检查是否有新版本（不安装；配合 --channel/--pre 选择渠道）")
	root.Flags().BoolVar(&pre, "pre", false, "与 --update 配合：包含预览版（等价于 --channel preview）")
	root.Flags().StringVar(&channel, "channel", "", "与 --update 配合的升级渠道：stable / preview / dev")
	root.RunE = func(c *cobra.Command, _ []string) error {
		if pre && !checkUpdate {
			return types.Errorf(types.CodeUsage, "--pre 需与 --update 一起使用")
		}
		if channel != "" && !checkUpdate {
			return types.Errorf(types.CodeUsage, "--channel 需与 --update 或 upgrade 一起使用")
		}
		if checkUpdate {
			return runCheckUpdate(c, version, channel, pre)
		}
		return c.Help()
	}

	root.SetVersionTemplate("dtool version " + info.Summary() + "\n")
	root.AddCommand(newVersionCmd(info), newUpgradeCmd(version), newConvertCmd(), newQueryCmd(),
		newVisualizeCmd(), newPipelineCmd(), newActionsCmd(), newDatasetsCmd(), newReapCmd(), newMemInfoCmd())

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

// openWorkspaceRaw 只打开工作区，不做任何回收——`actions sync` 需要它：
// 回收正是那条命令要做的事，入口先做掉的话它永远无事可做。
func openWorkspaceRaw() (*workspace.Workspace, error) {
	ws, err := workspace.Open(g.workspace)
	if err != nil {
		return nil, types.Errorf(types.CodeGeneral, "open workspace: %v", err)
	}
	return ws, nil
}

// openWorkspace 打开工作区并回收被强杀中断的 Action（进程已死的 running -> stale）。
// 放在这里是为了让每条命令都顺手收敛一次，避免读 `.dtool/actions/<id>.json` 的人
// 以为任务还在跑；想显式看收敛结果用 `dtool actions sync`。
func openWorkspace() (*workspace.Workspace, error) {
	ws, err := openWorkspaceRaw()
	if err != nil {
		return nil, err
	}
	if n, err := (&action.Recorder{WS: ws}).Reconcile(); err != nil {
		fmt.Fprintf(os.Stderr, "警告：回收被中断的 Action 失败：%v\n", err)
	} else if n > 0 {
		fmt.Fprintf(os.Stderr, "已把 %d 个被中断的任务（进程已消失）标记为 stale。\n", n)
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
	if !g.noRecord {
		// 起看护进程：本进程被强杀时由它把遗留的 running 收敛为 stale。
		// 直接在启动时做，而不是等第一条 Action，这样连「还没写 Action 就被杀」也覆盖不到，
		// 但要保证有工作区可回写。
		action.StartReaper(ws)
	}
	return &pipeline.Env{
		Ctx: ctx, WS: ws, Rec: &action.Recorder{WS: ws},
		Meta:    action.Metadata{Tags: tags, Notes: g.notes},
		Preview: g.previewRows, NoRecord: g.noRecord, Command: commandLine(), MaxMemory: parsedMaxMemory,
		LoadMode: g.loadMode, Store: g.store, MemPolicy: g.memPolicy,
		// --no-record 表示「不留痕迹」，选档历史也一并跳过
		PlanFile: planFile(g.noRecord, g.workspace),
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

// warnInertLoadMode 提前说明 --load-mode 的适用范围：它管的是 JSON → 内存 SQLite 的装入
// （只有 query / pipeline 会用到）。发给 convert 这类命令时会被静静忽略，先说出来，
// 免得用户以为「换个参数就能省内存」而白试一轮。
func warnInertLoadMode(c *cobra.Command) {
	if !c.Flags().Changed("load-mode") {
		return
	}
	switch c.Name() {
	case "query", "pipeline":
		return
	}
	fmt.Fprintf(c.ErrOrStderr(),
		"注意：--load-mode 只对 JSON 装入（query / pipeline）生效，%s 不使用该参数，已忽略。\n", c.Name())
}

func loadConfig(c *cobra.Command, _ []string) error {
	cfgFont = ""
	if g.loadMode == "" {
		g.loadMode = "auto"
	}
	if g.loadMode != "" {
		m, err := query.ParseLoadMode(g.loadMode)
		if err != nil {
			return err
		}
		g.loadMode = string(m)
	}
	// --store / --mem-policy 是操作者「强行指定执行方式」的入口：这里只校验取值，
	// 组合是否成立（如 full+disk）留给选档处报错，那里能给出更具体的建议。
	if g.store == "" {
		g.store = "auto"
	}
	if st, err := query.ParseStore(g.store); err != nil {
		return err
	} else {
		g.store = string(st)
	}
	if g.memPolicy == "" {
		g.memPolicy = "try"
	}
	if mp, err := memguard.ParsePolicy(g.memPolicy); err != nil {
		return types.Errorf(types.CodeUsage, "%v", err).WithHint("try 与 strict 二选一")
	} else {
		g.memPolicy = string(mp)
	}
	if g.maxMemory != "" {
		n, err := memguard.ParseBytes(g.maxMemory)
		if err != nil {
			return err
		}
		parsedMaxMemory = &n
	}
	warnInertLoadMode(c)
	if g.config == "" {
		return nil
	}
	cfg, err := config.Load(g.config)
	if err != nil {
		return err
	}
	cfgFont = cfg.Font
	if cfg.LoadMode != "" && !c.Flags().Changed("load-mode") {
		g.loadMode = cfg.LoadMode
	}
	if cfg.Store != "" && !c.Flags().Changed("store") {
		g.store = cfg.Store
	}
	if cfg.MemPolicy != "" && !c.Flags().Changed("mem-policy") {
		g.memPolicy = cfg.MemPolicy
	}
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

// planFile 返回选档历史文件；--no-record 时不读不写（与 Action 一致：不留痕迹）。
func planFile(noRecord bool, workspace string) string {
	if noRecord {
		return ""
	}
	return memguard.PlanFile(workspace)
}
