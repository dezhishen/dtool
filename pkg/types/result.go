package types

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// 退出码约定。
const (
	CodeGeneral  = 1
	CodeUsage    = 2
	CodeNotFound = 3
	CodeExec     = 4
)

// Row 是保持列顺序的一行数据。
type Row struct {
	Columns []string
	Values  []any
}

func (r Row) Get(name string) (any, bool) {
	for i, c := range r.Columns {
		if c == name {
			return r.Values[i], true
		}
	}
	return nil, false
}

func (r Row) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, c := range r.Columns {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(c)
		b.Write(k)
		b.WriteByte(':')
		v, err := json.Marshal(r.Values[i])
		if err != nil {
			return nil, err
		}
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func (r *Row) UnmarshalJSON(data []byte) error {
	if string(bytes.TrimSpace(data)) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return fmt.Errorf("row must be a JSON object")
	}
	r.Columns, r.Values = nil, nil
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return err
		}
		var v any
		if err := dec.Decode(&v); err != nil {
			return err
		}
		r.Columns = append(r.Columns, kt.(string))
		r.Values = append(r.Values, v)
	}
	_, err := dec.Token()
	return err
}

type QueryResult struct {
	Success    bool     `json:"success"`
	Columns    []string `json:"columns"`
	Rows       []Row    `json:"rows"`
	RowCount   int      `json:"row_count"`
	ResultFile string   `json:"result_file,omitempty"`
	OutputFile string   `json:"output_file,omitempty"`
	ActionID   string   `json:"action_id,omitempty"`
}

type ConvertResult struct {
	Success     bool      `json:"success"`
	Name        string    `json:"name"`
	UpdatedAt   time.Time `json:"updated_at"`
	DataFile    string    `json:"data_file"`
	SchemaFile  string    `json:"schema_file"`
	RecordCount int       `json:"record_count"`
	Columns     []string  `json:"columns"`
	Warnings    []string  `json:"warnings,omitempty"`
	ActionID    string    `json:"action_id,omitempty"`
}

type VisualizeResult struct {
	Success  bool     `json:"success"`
	File     string   `json:"file"`
	Type     string   `json:"type"`
	X        string   `json:"x,omitempty"`
	Y        string   `json:"y,omitempty"`
	Points   int      `json:"points"`
	Warnings []string `json:"warnings,omitempty"`
	ActionID string   `json:"action_id,omitempty"`
}

type PipelineResult struct {
	Success  bool             `json:"success"`
	Convert  *ConvertResult   `json:"convert,omitempty"`
	Query    *QueryResult     `json:"query,omitempty"`
	Chart    *VisualizeResult `json:"chart,omitempty"`
	ActionID string           `json:"action_id,omitempty"`
}

type ErrorResponse struct {
	Error    string `json:"error"`
	Detail   string `json:"detail,omitempty"`
	Hint     string `json:"hint,omitempty"`
	Code     int    `json:"code"`
	ActionID string `json:"action_id,omitempty"`
}

// Error 是带退出码的错误。
type Error struct {
	Code     int
	Message  string
	Detail   string
	Hint     string
	ActionID string
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Response() ErrorResponse {
	return ErrorResponse{Error: e.Message, Detail: e.Detail, Hint: e.Hint, Code: e.Code, ActionID: e.ActionID}
}

func Errorf(code int, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...)}
}

func (e *Error) WithDetail(s string) *Error { e.Detail = s; return e }
func (e *Error) WithHint(s string) *Error   { e.Hint = s; return e }

// AsError 把任意错误规范化为 *Error，非 *Error 时使用 def 作为退出码。
func AsError(err error, def int) *Error {
	var te *Error
	if errors.As(err, &te) {
		return te
	}
	return &Error{Code: def, Message: err.Error()}
}
