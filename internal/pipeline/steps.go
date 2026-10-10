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
func (e *Env) run(typ string, input map[string]any, parent, derived string,
	fn func(id, dir string) (*action.Output, error)) (*action.Action, error) {
	var a *action.Action
	var dir string
	if e.NoRecord {
		d, err := os.MkdirTemp("", "dtool-*")
		if err != nil {
			return nil, err
		}
		a, dir = &action.Action{Type: typ}, d
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
	out, err := fn(a.ID, dir)
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
		parentID, "", func(id, _ string) (*action.Output, error) {
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
			m, err := ds.Commit(stage, dataset.Meta{Name: name, ActionID: id, Source: p.Input, Sheet: r.Sheet, UpdatedAt: now,
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

type QueryParams struct {
	SQL, Format, Output string
	Sources             map[string]string // 别名 -> 引用（文件 / action:<id> / latest:<type>）
	From                string            // 仅用于血缘
	MaxRows             int
	Timeout             time.Duration
	Sandbox             bool
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
	var res *types.QueryResult
	a, err := e.run("query", input, parentID, derived, func(_, dir string) (*action.Output, error) {
		r, err := query.Run(e.Ctx, query.Options{SQL: p.SQL, Sources: sources,
			Roots: []string{e.WS.Root, cwd}, Lookup: (&dataset.Store{WS: e.WS}).Lookup,
			Sandbox: p.Sandbox, MaxRows: p.MaxRows, Timeout: p.Timeout, MaxMemory: e.MaxMemory,
			LoadMode: e.LoadMode, Store: e.Store, MemPolicy: e.MemPolicy, PlanFile: e.PlanFile})
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
	Input, Type, X, Y, Title, Format, Output, Font, From string
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
	input := map[string]any{"input": p.Input, "type": p.Type, "x": p.X, "y": p.Y, "title": p.Title, "format": p.Format}
	var res *types.VisualizeResult
	a, err := e.run("visualize", input, parentID, derived, func(_, dir string) (*action.Output, error) {
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
		vr, err := visualize.Render(cols, rows, visualize.Options{Type: p.Type, X: p.X, Y: p.Y,
			Title: p.Title, Format: p.Format, FontPath: p.Font, OutFile: file})
		if err != nil {
			return nil, err
		}
		res = &types.VisualizeResult{Success: true, File: e.WS.Rel(file), Type: p.Type, X: p.X, Y: p.Y,
			Points: vr.Points, Warnings: vr.Warnings}
		return &action.Output{Files: []string{res.File}, Warnings: vr.Warnings,
			Summary: fmt.Sprintf("生成 %s 图表：%d 个数据点", p.Type, vr.Points),
			Details: map[string]any{"type": p.Type, "x": p.X, "y": p.Y, "points": vr.Points, "font": vr.Font}}, nil
	})
	if err != nil {
		return nil, err
	}
	res.ActionID = a.ID
	return res, nil
}
