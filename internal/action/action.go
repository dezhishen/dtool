package action

import (
	"time"

	"github.com/dezhishen/dtool/pkg/types"
)

const (
	StatusRunning = "running"
	StatusSuccess = "success"
	StatusFailed  = "failed"
	StatusStale   = "stale"
)

type Annotation struct {
	At   time.Time `json:"at"`
	By   string    `json:"by"`
	Text string    `json:"text"`
}

type Output struct {
	Files            []string       `json:"files,omitempty"`
	RowCount         int            `json:"row_count,omitempty"`
	Columns          []string       `json:"columns,omitempty"`
	SchemaRef        string         `json:"schema_ref,omitempty"`
	Preview          []types.Row    `json:"preview,omitempty"`
	PreviewTruncated bool           `json:"preview_truncated,omitempty"`
	Summary          string         `json:"summary"`
	Details          map[string]any `json:"details,omitempty"`
	Warnings         []string       `json:"warnings,omitempty"`
}

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
	Hint    string `json:"hint,omitempty"`
}

type Metadata struct {
	Tags  []string `json:"tags,omitempty"`
	Notes string   `json:"notes,omitempty"`
}

type Action struct {
	ID          string         `json:"id"`
	Type        string         `json:"type"`
	Status      string         `json:"status"`
	StartedAt   time.Time      `json:"started_at"`
	FinishedAt  *time.Time     `json:"finished_at,omitempty"`
	DurationMs  int64          `json:"duration_ms,omitempty"`
	Pid         int            `json:"pid,omitempty"`
	Command     string         `json:"command"`
	Input       map[string]any `json:"input"`
	Output      *Output        `json:"output,omitempty"`
	ParentID    string         `json:"parent_id,omitempty"`
	DerivedFrom string         `json:"derived_from,omitempty"`
	Annotations []Annotation   `json:"annotations,omitempty"`
	Error       *Error         `json:"error"`
	Metadata    Metadata       `json:"metadata"`
}

// Preview 截取前 n 行作为预览。
func Preview(rows []types.Row, n int) ([]types.Row, bool) {
	if n <= 0 {
		n = 20
	}
	if len(rows) <= n {
		return rows, false
	}
	return rows[:n], true
}

// MainFile 返回可作为数据源的主产物（相对路径）。
func (a *Action) MainFile() string {
	if a.Output == nil || len(a.Output.Files) == 0 {
		return ""
	}
	return a.Output.Files[0]
}
