package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dezhishen/dtool/internal/action"
	"github.com/dezhishen/dtool/internal/converter"
	"github.com/dezhishen/dtool/internal/dataset"
	"github.com/dezhishen/dtool/internal/formatter"
	"github.com/dezhishen/dtool/internal/memguard"
	"github.com/dezhishen/dtool/internal/query"
	"github.com/dezhishen/dtool/internal/visualize"
	"github.com/dezhishen/dtool/internal/workspace"
	"github.com/dezhishen/dtool/pkg/types"
)

// Env 聚合各步骤共享的运行环境。
type Env struct {
	Ctx      context.Context
	WS       *workspace.Workspace
	Rec      *action.Recorder
	Meta     action.Metadata
	Preview  int
	NoRecord bool
	Command  string
	// MaxMemory 内存预算：nil 自动探测，指向 0 表示关闭检查（--max-memory）。
	MaxMemory *uint64
	// LoadMode 传给查询的装入方式（auto / stream / full）。
	LoadMode string
	// Store 传给查询的落库位置（auto / memory / disk）；MemPolicy 决定「都放不下」时
	// 是仍试最省档（try）还是直接失败（strict）；PlanFile 是选档历史文件（可空）。
	Store     string
	MemPolicy string
	PlanFile  string
	// Engine 传给查询的后端名（auto / 已注册引擎，见 query.ResolveEngine）；
	// EngineOpts 是该后端在配置文件 db.engine.<name> 下的全部配置。
	Engine     string
	EngineOpts map[string]any
}

// Excel 转换的内存预估（流式转换后实测：13KB→18.5MB、7.4MB→47MB、17.8MB→76MB，
// 即固定开销 + 文件 × ~3.2）。预检取 ~1.7 倍余量，避免低估导致用户被内核 OOM 杀掉。
const (
	xlsxBaseOverhead = 32 << 20 // 运行时与 excelize 的固定开销
	xlsxPeakFactor   = 6        // 每 MB 输入的内存系数
)

// xlsxNeed 返回 Excel 转换的预估峰值内存；预检与性能回归测试共用同一口径。
func xlsxNeed(size uint64) uint64 { return xlsxBaseOverhead + size*xlsxPeakFactor }

// run 负责 Action 的 Start/Success/Fail 生命周期与产物目录。
// 回调拿到 Action 指针，可在落盘前补充 input（例如可视化后才确定的系列名）。
func (e *Env) run(typ string, input map[string]any, parent, derived string,
	fn func(a *action.Action, dir string) (*action.Output, error)) (*action.Action, error) {
	var a *action.Action
	var dir string
	if e.NoRecord {
		d, err := os.MkdirTemp("", "dtool-*")
		if err != nil {
			return nil, err
		}
		a, dir = &action.Action{Type: typ, Input: input}, d
	} else {
		var err error
		a, err = e.Rec.Start(action.StartParams{Type: typ, Input: input, Command: e.Command,
			ParentID: parent, DerivedFrom: derived, Metadata: e.Meta})
		if err != nil {
			return nil, err
		}
		if typ != "convert" { // 数据集写入 datasets/，不属于 Action 产物
			if dir, err = e.WS.ActionOutputDir(typ, a.ID); err != nil {
				return a, err
			}
		}
	}
	out, err := fn(a, dir)
	if err != nil {
		te := types.AsError(err, types.CodeExec)
		if !e.NoRecord {
			te.ActionID = a.ID
			_ = e.Rec.Fail(a, te)
		}
		return a, te
	}
	if !e.NoRecord {
		if err := e.Rec.Success(a, out); err != nil {
			return a, err
		}
	}
	return a, nil
}

type ConvertParams struct {
	Input, Sheet string
	Name         string // 数据集名，缺省取输入文件名（指定 --sheet 时追加 _<sheet>）
}

// DefaultDatasetName 由输入文件名（及工作表）生成数据集名。
func DefaultDatasetName(input, sheet string) string {
	base := strings.TrimSuffix(filepath.Base(input), filepath.Ext(input))
	if sheet != "" {
		base += "_" + sheet
	}
	return workspace.SafeName(base)
}

// Convert 把 Excel 转换为独立的数据集（datasets/<name>/），同名则覆盖并递增版本。
func (e *Env) Convert(p ConvertParams, parentID string) (*types.ConvertResult, error) {
	name := p.Name
	if name == "" {
		name = DefaultDatasetName(p.Input, p.Sheet)
	}
	name = workspace.SafeName(name)
	ds := &dataset.Store{WS: e.WS}
	var res *types.ConvertResult
	a, err := e.run("convert", map[string]any{"input": p.Input, "sheet": p.Sheet, "name": name},
		parentID, "", func(a *action.Action, _ string) (*action.Output, error) {
			// 放进回调内，内存不足时同样留下一条 failed 的 Action，便于事后排查
			size := memguard.SizeOf(p.Input)
			mem := memguard.Budget(e.MaxMemory)
			need := xlsxNeed(size)
			checkErr := memguard.CheckNeed("Excel 文件", size, need,
				fmt.Sprintf("%.0fMB 固定开销 + 文件 × %d 的流式估算", float64(xlsxBaseOverhead)/(1<<20), xlsxPeakFactor),
				mem, memguard.HintSplitInput)
			memguard.Debugf("Excel %s（%s）：%s；预计需 %s；预检=%s", filepath.Base(p.Input),
				memguard.HumanSize(size), mem.Describe(), memguard.HumanSize(need), memguard.Verdict(checkErr))
			if checkErr != nil {
				return nil, checkErr
			}
			memguard.Progress(os.Stderr, memguard.DefaultProgressMinSize, filepath.Base(p.Input), size, need, "流式解析 xlsx")
			stage, err := ds.NewStaging()
			if err != nil {
				return nil, err
			}
			defer os.RemoveAll(stage)
			now := time.Now().UTC().Truncate(time.Second) // 与 Schema 中的秒级 updated_at 保持一致
			r, err := converter.ConvertExcel(converter.Options{Ctx: e.Ctx, Input: p.Input, Sheet: p.Sheet, OutDir: stage,
				UpdatedAt: now, PreviewRows: e.Preview})
			if err != nil {
				return nil, err
			}
			m, err := ds.Commit(stage, dataset.Meta{Name: name, ActionID: a.ID, Source: p.Input, Sheet: r.Sheet, UpdatedAt: now,
				RecordCount: r.RecordCount, Columns: r.Columns})
			if err != nil {
				return nil, err
			}
			info, err := ds.Info(name)
			if err != nil {
				return nil, err
			}
			// 预览由转换阶段顺带留下（流式转换不再持有全部记录），
			// 是否截断只能按总记录数判断。
			prev, trunc := r.Preview, r.RecordCount > len(r.Preview)
			out := &action.Output{
				Files:    []string{info.DataFile},
				RowCount: r.RecordCount, Columns: r.Columns,
				Preview: prev, PreviewTruncated: trunc,
				Summary:  fmt.Sprintf("转换 %s → 数据集 %s（%d 条记录）", filepath.Base(p.Input), name, r.RecordCount),
				Warnings: r.Warnings,
				Details:  map[string]any{"dataset": name},
			}
			res = &types.ConvertResult{Success: true, Name: name, UpdatedAt: m.UpdatedAt, DataFile: info.DataFile,
				RecordCount: r.RecordCount, Columns: r.Columns, Warnings: r.Warnings}
			out.Files = append(out.Files, info.SchemaFile)
			out.SchemaRef = info.SchemaFile
			res.SchemaFile = info.SchemaFile
			return out, nil
		})
	if err != nil {
		return nil, err
	}
	res.ActionID = a.ID
	return res, nil
}

type DatasetFromResultParams struct {
	Name string // 新数据集名
	From string // 结果集引用：action:<id> / latest:query
}

// DatasetFromResult 把一次 query 的结果集提升为独立数据集（data.json + Schema），
// 并把这次提升记录成一条 convert Action，derived_from 指向源 query Action。
func (e *Env) DatasetFromResult(p DatasetFromResultParams, parentID string) (*types.ConvertResult, error) {
	if p.Name == "" {
		return nil, types.Errorf(types.CodeUsage, "missing dataset name")
	}
	if p.From == "" {
		return nil, types.Errorf(types.CodeUsage, "missing --from（结果集引用）")
	}
	name := workspace.SafeName(p.Name)
	r, err := e.Rec.Resolve(p.From)
	if err != nil {
		return nil, err
	}
	if r.ActionID == "" {
		return nil, types.Errorf(types.CodeUsage, "--from 必须是结果集引用（action:<id> / latest:query）")
	}
	src, err := e.Rec.Get(r.ActionID)
	if err != nil {
		return nil, err
	}
	if src.Type != "query" {
		return nil, types.Errorf(types.CodeUsage, "只能从 query 结果集生成数据集，当前 Action 类型是 %s", src.Type).
			WithHint("先用 dtool query 产出结果集，再执行 datasets create")
	}
	if src.Output == nil || len(src.Output.Files) == 0 {
		return nil, types.Errorf(types.CodeNotFound, "源 Action %s 没有结果文件", src.ID)
	}

	cols, rows, err := visualize.LoadRows(r.Path)
	if err != nil {
		return nil, err
	}
	var headers []string
	var schema []converter.ColumnSchema
	var norm []types.Row
	if len(rows) > 0 {
		headers, schema, norm = converter.InferSchemaFromRows(rows)
	} else {
		// 空结果集仍可用 Action 记录的列名建立空数据集。
		headers = cols
		if len(headers) == 0 {
			headers = src.Output.Columns
		}
		data := make([][]string, len(headers))
		schema = converter.InferSchema(headers, data, 0)
		norm = []types.Row{}
	}
	if len(headers) == 0 {
		return nil, types.Errorf(types.CodeExec, "结果集没有列，无法生成数据集")
	}

	input := map[string]any{"from": p.From, "name": name, "source_action": src.ID, "purpose": "dataset_from_result"}
	var res *types.ConvertResult
	a, err := e.run("convert", input, parentID, src.ID, func(a *action.Action, _ string) (*action.Output, error) {
		ds := &dataset.Store{WS: e.WS}
		stage, err := ds.NewStaging()
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(stage)
		now := time.Now().UTC().Truncate(time.Second)
		if err := workspace.WriteJSONAtomic(filepath.Join(stage, dataset.DataFile), norm); err != nil {
			return nil, err
		}
		doc := map[string]any{
			"source":       "action:" + src.ID,
			"sheet":        "",
			"record_count": len(norm),
			"updated_at":   now.Format(time.RFC3339),
			"columns":      schema,
		}
		if err := workspace.WriteJSONAtomic(filepath.Join(stage, dataset.SchemaFile), doc); err != nil {
			return nil, err
		}
		m, err := ds.Commit(stage, dataset.Meta{Name: name, ActionID: a.ID,
			Source: "action:" + src.ID, UpdatedAt: now, RecordCount: len(norm), Columns: headers})
		if err != nil {
			return nil, err
		}
		info, err := ds.Info(name)
		if err != nil {
			return nil, err
		}
		prev, trunc := action.Preview(norm, e.Preview)
		res = &types.ConvertResult{Success: true, Name: name, UpdatedAt: m.UpdatedAt,
			DataFile: info.DataFile, SchemaFile: info.SchemaFile, RecordCount: len(norm), Columns: headers}
		return &action.Output{
			Files: []string{info.DataFile, info.SchemaFile}, RowCount: len(norm), Columns: headers,
			SchemaRef: info.SchemaFile, Preview: prev, PreviewTruncated: trunc,
			Summary: fmt.Sprintf("从结果集 %s 生成数据集 %s（%d 行）", src.ID, name, len(norm)),
			Details: map[string]any{"dataset": name, "source_action": src.ID},
		}, nil
	})
	if err != nil {
		return nil, err
	}
	res.ActionID = a.ID
	return res, nil
}

type QueryParams struct {
	SQL, Format, Output string
	Sources             map[string]string // 别名 -> 引用（文件 / action:<id> / latest:<type>）
	From                string            // 仅用于血缘
	MaxRows             int
	Timeout             time.Duration
	Sandbox             bool
	Extra               map[string]any // 额外写入 Action input 的字段（如 purpose/column）
}

func (e *Env) lineage(from string) (string, error) {
	if from == "" {
		return "", nil
	}
	r, err := e.Rec.Resolve(from)
	if err != nil {
		return "", err
	}
	return r.ActionID, nil
}

func (e *Env) Query(p QueryParams, parentID string) (*types.QueryResult, error) {
	if p.Format == "xlsx" && p.Output == "" {
		return nil, types.Errorf(types.CodeUsage, "--format xlsx 是二进制格式，必须指定 --output")
	}
	derived, err := e.lineage(p.From)
	if err != nil {
		return nil, err
	}
	sources := map[string]string{}
	recorded := map[string]string{}
	for alias, ref := range p.Sources {
		r, err := e.Rec.Resolve(ref)
		if err != nil {
			return nil, err
		}
		abs, err := filepath.Abs(r.Path)
		if err != nil {
			return nil, err
		}
		sources[alias] = abs
		recorded[alias] = e.WS.Rel(abs)
	}
	format := p.Format
	if format == "" {
		format = "json"
	}
	cwd, _ := os.Getwd()
	input := map[string]any{"sql": p.SQL, "sources": recorded, "format": format}
	for k, v := range p.Extra {
		input[k] = v
	}
	var res *types.QueryResult
	a, err := e.run("query", input, parentID, derived, func(_ *action.Action, dir string) (*action.Output, error) {
		r, err := query.Run(e.Ctx, query.Options{SQL: p.SQL, Sources: sources,
			Roots: []string{e.WS.Root, cwd}, Lookup: (&dataset.Store{WS: e.WS}).Lookup,
			Sandbox: p.Sandbox, MaxRows: p.MaxRows, Timeout: p.Timeout, MaxMemory: e.MaxMemory,
			LoadMode: e.LoadMode, Store: e.Store, MemPolicy: e.MemPolicy, PlanFile: e.PlanFile,
			EngineName: e.Engine, EngineOpts: e.EngineOpts})
		if err != nil {
			return nil, err
		}
		resFile := filepath.Join(dir, "result.json")
		if err := workspace.WriteJSONAtomic(resFile, r.Rows); err != nil {
			return nil, err
		}
		files := []string{e.WS.Rel(resFile)}
		r.ResultFile = files[0]
		if p.Output != "" {
			if err := writeFormatted(p.Output, format, r); err != nil {
				return nil, err
			}
			files = append(files, e.WS.Rel(p.Output))
			r.OutputFile = e.WS.Rel(p.Output)
		}
		prev, trunc := action.Preview(r.Rows, e.Preview)
		res = r
		return &action.Output{Files: files, RowCount: r.RowCount, Columns: r.Columns,
			Preview: prev, PreviewTruncated: trunc, Summary: fmt.Sprintf("查询返回 %d 行", r.RowCount)}, nil
	})
	if err != nil {
		return nil, err
	}
	res.ActionID = a.ID
	return res, nil
}

func writeFormatted(path, format string, r *types.QueryResult) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return formatter.Write(f, format, r)
}

type VisualizeParams struct {
	Input, Type, X, GroupBy, Missing, Title, Format, Output, Font, From string
	Y                                                                   []string
}

func (e *Env) Visualize(p VisualizeParams, parentID string) (*types.VisualizeResult, error) {
	r, err := e.Rec.Resolve(p.Input)
	if err != nil {
		return nil, err
	}
	derived := r.ActionID
	if p.From != "" {
		if derived, err = e.lineage(p.From); err != nil {
			return nil, err
		}
	}
	if p.Format == "" {
		p.Format = "png"
	}
	input := map[string]any{"input": p.Input, "type": p.Type, "x": p.X,
		"title": p.Title, "format": p.Format}
	if len(p.Y) != 0 {
		input["y"] = p.Y
	}
	if p.GroupBy != "" {
		input["group_by"] = p.GroupBy
	}
	if p.Missing != "" && p.Missing != "zero" {
		input["missing"] = p.Missing
	}
	var res *types.VisualizeResult
	a, err := e.run("visualize", input, parentID, derived, func(act *action.Action, dir string) (*action.Output, error) {
		cols, rows, err := visualize.LoadRows(r.Path)
		if err != nil {
			return nil, err
		}
		file := filepath.Join(dir, "chart."+visualize.Ext(p.Type, p.Format))
		if p.Output != "" {
			file = p.Output
			if d := filepath.Dir(file); d != "." {
				if err := os.MkdirAll(d, 0o755); err != nil {
					return nil, err
				}
			}
		}
		vr, err := visualize.Render(cols, rows, visualize.Options{Type: p.Type, X: p.X, Y: p.Y, GroupBy: p.GroupBy,
			Missing: p.Missing, Title: p.Title, Format: p.Format, FontPath: p.Font, OutFile: file})
		if err != nil {
			return nil, err
		}
		if len(vr.Series) > 0 {
			// 系列名只有在渲染（透视/多列校验）之后才能确定，这里在 Action 落盘前补进 input。
			act.Input["series"] = vr.Series
			act.Input["series_count"] = len(vr.Series)
		}
		res = &types.VisualizeResult{Success: true, File: e.WS.Rel(file), Type: p.Type, X: p.X, Y: p.Y,
			GroupBy: p.GroupBy, Missing: vr.Missing, Series: vr.Series, Points: vr.Points, Warnings: vr.Warnings}
		details := map[string]any{"type": p.Type, "x": p.X, "points": vr.Points, "font": vr.Font}
		if len(p.Y) != 0 {
			details["y"] = p.Y
		}
		if p.GroupBy != "" {
			details["group_by"] = p.GroupBy
			details["series"] = vr.Series
		}
		if vr.Missing != "" {
			details["missing"] = vr.Missing
		}
		return &action.Output{Files: []string{res.File}, Warnings: vr.Warnings,
			Summary: fmt.Sprintf("生成 %s 图表：%d 个数据点", p.Type, vr.Points),
			Details: details}, nil
	})
	if err != nil {
		return nil, err
	}
	res.ActionID = a.ID
	return res, nil
}
