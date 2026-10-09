package pipeline

import (
	"fmt"
	"strings"
	"time"

	"github.com/dezhishen/dtool/internal/action"
	"github.com/dezhishen/dtool/pkg/types"
)

type Config struct {
	Excel, Sheet, SQL string
	Name              string // 数据集名（仅 --excel 时有效）
	Input             string // 复用已持久化的数据（文件 / action:<id> / latest:<type>），与 Excel 二选一
	Chart, X, Y       string
	Title, Format     string
	Font              string
	Output            string
	MaxRows           int
	Timeout           time.Duration
	Sandbox           bool
}

// Pipeline 依次执行 convert → query → visualize，并用父 Action 关联三个子 Action。
// 子步骤通过文件路径传递数据，血缘通过 --from 语义记录，保证每个子 Action 可单独复现。
func (e *Env) Pipeline(c Config) (*types.PipelineResult, error) {
	if c.Excel == "" && c.Input == "" {
		return nil, types.Errorf(types.CodeUsage, "需要 --excel（转换新数据）或 --input（复用已持久化的数据）")
	}
	if c.Excel != "" && c.Input != "" {
		return nil, types.Errorf(types.CodeUsage, "--excel 与 --input 不能同时使用")
	}
	if c.SQL == "" && c.Chart != "" {
		return nil, types.Errorf(types.CodeUsage, "--chart 需要 --sql 提供数据")
	}

	var parent *action.Action
	if !e.NoRecord {
		var err error
		parent, err = e.Rec.Start(action.StartParams{Type: "pipeline", Command: e.Command, Metadata: e.Meta,
			Input: map[string]any{"excel": c.Excel, "input": c.Input, "sql": c.SQL, "chart": c.Chart, "x": c.X, "y": c.Y}})
		if err != nil {
			return nil, err
		}
	}
	pid := ""
	if parent != nil {
		pid = parent.ID
	}
	res := &types.PipelineResult{ActionID: pid}
	var children []string
	var steps []string

	fail := func(step string, err error) (*types.PipelineResult, error) {
		te := types.AsError(err, types.CodeExec)
		wrapped := &types.Error{Code: te.Code, Message: te.Message, Hint: te.Hint, ActionID: te.ActionID,
			Detail: fmt.Sprintf("failed step: %s (action %s). %s", step, te.ActionID, te.Detail)}
		if parent != nil {
			_ = e.Rec.Fail(parent, wrapped)
		}
		return nil, wrapped
	}

	var dataPath, dataActionID string
	if c.Excel != "" {
		conv, err := e.Convert(ConvertParams{Input: c.Excel, Sheet: c.Sheet, Name: c.Name}, pid)
		if err != nil {
			return fail("convert", err)
		}
		res.Convert = conv
		children = append(children, conv.ActionID)
		steps = append(steps, "convert")
		dataPath, dataActionID = e.WS.Abs(conv.DataFile), conv.ActionID
	} else {
		r, err := e.Rec.Resolve(c.Input)
		if err != nil {
			return fail("resolve-input", err)
		}
		dataPath, dataActionID = r.Path, r.ActionID
	}

	if c.SQL != "" {
		q, err := e.Query(QueryParams{SQL: c.SQL, Format: "json",
			Sources: map[string]string{"data": dataPath, "data.json": dataPath},
			From:    fromRef(dataActionID), MaxRows: c.MaxRows, Timeout: c.Timeout, Sandbox: c.Sandbox}, pid)
		if err != nil {
			return fail("query", err)
		}
		res.Query = q
		children = append(children, q.ActionID)
		steps = append(steps, "query")

		if c.Chart != "" {
			ch, err := e.Visualize(VisualizeParams{Input: e.WS.Abs(q.ResultFile), From: fromRef(q.ActionID),
				Type: c.Chart, X: c.X, Y: c.Y, Title: c.Title, Format: c.Format, Output: c.Output, Font: c.Font}, pid)
			if err != nil {
				return fail("visualize", err)
			}
			res.Chart = ch
			children = append(children, ch.ActionID)
			steps = append(steps, "visualize")
		}
	}

	res.Success = true
	if parent != nil {
		err := e.Rec.Success(parent, &action.Output{
			Summary: fmt.Sprintf("pipeline 完成：%s", strings.Join(steps, " → ")),
			Details: map[string]any{"children": children, "steps": steps, "data": e.WS.Rel(dataPath)},
		})
		if err != nil {
			return nil, err
		}
	}
	return res, nil
}

func fromRef(id string) string {
	if id == "" {
		return ""
	}
	return "action:" + id
}
