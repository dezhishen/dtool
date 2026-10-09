package action

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"time"

	"github.com/dezhishen/dtool/internal/dataset"
	"github.com/dezhishen/dtool/internal/workspace"
	"github.com/dezhishen/dtool/pkg/types"
	"github.com/oklog/ulid/v2"
)

var idRe = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// ValidID 校验 ULID 格式，防止把外部输入拼进路径时发生穿越。
func ValidID(id string) bool { return idRe.MatchString(id) }

func NewID() string { return ulid.Make().String() }

type Recorder struct {
	WS *workspace.Workspace
}

type StartParams struct {
	Type        string
	Input       map[string]any
	Command     string
	ParentID    string
	DerivedFrom string
	Metadata    Metadata
}

func (r *Recorder) Start(p StartParams) (*Action, error) {
	a := &Action{
		ID:          NewID(),
		Type:        p.Type,
		Status:      StatusRunning,
		StartedAt:   time.Now().UTC(),
		Pid:         os.Getpid(),
		Command:     p.Command,
		Input:       p.Input,
		ParentID:    p.ParentID,
		DerivedFrom: p.DerivedFrom,
		Metadata:    p.Metadata,
	}
	return a, r.save(a)
}

func (r *Recorder) Success(a *Action, out *Output) error {
	a.Status = StatusSuccess
	a.Output = out
	r.finish(a)
	return r.save(a)
}

func (r *Recorder) Fail(a *Action, err error) error {
	te := types.AsError(err, types.CodeExec)
	a.Status = StatusFailed
	a.Error = &Error{Code: te.Code, Message: te.Message, Detail: te.Detail, Hint: te.Hint}
	r.finish(a)
	return r.save(a)
}

func (r *Recorder) finish(a *Action) {
	now := time.Now().UTC()
	a.FinishedAt = &now
	a.DurationMs = now.Sub(a.StartedAt).Milliseconds()
}

func (r *Recorder) save(a *Action) error {
	unlock, err := r.WS.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	return r.saveLocked(a)
}

func (r *Recorder) saveLocked(a *Action) error {
	if err := workspace.WriteJSONAtomic(r.WS.ActionPath(a.ID), a); err != nil {
		return err
	}
	idx := r.loadIndex()
	idx.upsert(entryOf(a))
	return workspace.WriteJSONAtomic(r.WS.IndexPath(), idx)
}

func (r *Recorder) readRaw(id string) (*Action, error) {
	if !ValidID(id) {
		return nil, r.notFound(id)
	}
	var a Action
	if err := workspace.ReadJSON(r.WS.ActionPath(id), &a); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, r.notFound(id)
		}
		return nil, err
	}
	return &a, nil
}

// Get 返回用于展示的 Action（崩溃遗留的 running 显示为 stale）。
func (r *Recorder) Get(id string) (*Action, error) {
	a, err := r.readRaw(id)
	if err != nil {
		return nil, err
	}
	if a.Status == StatusRunning && !pidAlive(a.Pid) {
		a.Status = StatusStale
	}
	return a, nil
}

func (r *Recorder) notFound(id string) error {
	e := types.Errorf(types.CodeNotFound, "action not found: %s", id)
	entries, _, _ := r.List(Filter{Limit: 5})
	if len(entries) > 0 {
		s := ""
		for _, en := range entries {
			s += fmt.Sprintf("%s(%s) ", en.ID, en.Type)
		}
		e.Detail = "recent actions: " + s
	}
	return e
}

func (r *Recorder) Annotate(id, text, by string) (*Action, error) {
	unlock, err := r.WS.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	a, err := r.readRaw(id)
	if err != nil {
		return nil, err
	}
	a.Annotations = append(a.Annotations, Annotation{At: time.Now().UTC(), By: by, Text: text})
	return a, r.saveLocked(a)
}

type Filter struct {
	Type   string
	Status string
	Limit  int
}

// List 按时间倒序（最新在前）返回匹配的索引项及匹配总数。
func (r *Recorder) List(f Filter) ([]IndexEntry, int, error) {
	idx := r.loadIndex()
	var out []IndexEntry
	for i := len(idx.Actions) - 1; i >= 0; i-- {
		e := idx.Actions[i]
		e.Status = effectiveStatus(e.Status, e.Pid)
		if f.Type != "" && e.Type != f.Type {
			continue
		}
		if f.Status != "" && e.Status != f.Status {
			continue
		}
		out = append(out, e)
	}
	total := len(out)
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, total, nil
}

// Trace 返回某 Action 的上游祖先与下游后代（沿 parent_id / derived_from）。
func (r *Recorder) Trace(id string) (up, down []IndexEntry, err error) {
	if _, err := r.readRaw(id); err != nil {
		return nil, nil, err
	}
	idx := r.loadIndex()
	byID := map[string]IndexEntry{}
	for _, e := range idx.Actions {
		e.Status = effectiveStatus(e.Status, e.Pid)
		byID[e.ID] = e
	}
	seen := map[string]bool{id: true}
	for cur := byID[id]; ; {
		next := cur.DerivedFrom
		if next == "" {
			next = cur.ParentID
		}
		p, ok := byID[next]
		if !ok || seen[next] {
			break
		}
		seen[next] = true
		up = append([]IndexEntry{p}, up...)
		cur = p
	}
	set := map[string]bool{id: true}
	ids := make([]string, 0, len(byID))
	for k := range byID {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	for _, k := range ids {
		e := byID[k]
		if set[e.ID] {
			continue
		}
		if set[e.DerivedFrom] || set[e.ParentID] {
			set[e.ID] = true
			down = append(down, e)
		}
	}
	return up, down, nil
}

// All 读取全部 Action 完整内容（按 ID 升序）。
func (r *Recorder) All() ([]*Action, error) {
	files, err := os.ReadDir(r.WS.ActionsDir())
	if err != nil {
		return nil, err
	}
	var out []*Action
	for _, f := range files {
		name := f.Name()
		if len(name) != 31 || name[26:] != ".json" || !ValidID(name[:26]) {
			continue
		}
		a, err := r.Get(name[:26])
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Resolved 是数据源引用的解析结果。
type Resolved struct {
	ActionID string
	Path     string
}

// Resolve 解析 文件路径 / action:<id> / latest:<type> / dataset:<name> 引用。
func (r *Recorder) Resolve(ref string) (*Resolved, error) {
	switch {
	case hasPrefix(ref, "action:"):
		a, err := r.Get(ref[len("action:"):])
		if err != nil {
			return nil, err
		}
		return r.fromAction(a)
	case hasPrefix(ref, "dataset:"):
		ds := &dataset.Store{WS: r.WS}
		name := ref[len("dataset:"):]
		m, err := ds.Get(name)
		if err != nil {
			return nil, err
		}
		return &Resolved{ActionID: m.ActionID, Path: ds.Path(name)}, nil
	case hasPrefix(ref, "latest:"):
		typ := ref[len("latest:"):]
		entries, _, _ := r.List(Filter{Type: typ, Status: StatusSuccess, Limit: 1})
		if len(entries) == 0 {
			return nil, types.Errorf(types.CodeNotFound, "no successful %q action found", typ)
		}
		a, err := r.Get(entries[0].ID)
		if err != nil {
			return nil, err
		}
		return r.fromAction(a)
	}
	if _, err := os.Stat(ref); err != nil {
		return nil, types.Errorf(types.CodeNotFound, "file not found: %s", ref)
	}
	return &Resolved{Path: ref}, nil
}

func (r *Recorder) fromAction(a *Action) (*Resolved, error) {
	if a.Status != StatusSuccess {
		return nil, types.Errorf(types.CodeNotFound, "action %s is %s, not success", a.ID, a.Status)
	}
	f := a.MainFile()
	if f == "" {
		return nil, types.Errorf(types.CodeNotFound, "action %s has no data output", a.ID)
	}
	return &Resolved{ActionID: a.ID, Path: r.WS.Abs(f)}, nil
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }
