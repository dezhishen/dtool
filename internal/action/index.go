package action

import (
	"sort"
	"time"

	"github.com/dezhishen/dtool/internal/workspace"
)

type IndexEntry struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"`
	Status      string    `json:"status"`
	StartedAt   time.Time `json:"started_at"`
	Summary     string    `json:"summary"`
	ParentID    string    `json:"parent_id,omitempty"`
	DerivedFrom string    `json:"derived_from,omitempty"`
	Pid         int       `json:"pid,omitempty"`
}

type Index struct {
	WorkspaceID string       `json:"workspace_id"`
	CreatedAt   time.Time    `json:"created_at"`
	Actions     []IndexEntry `json:"actions"`
}

func effectiveStatus(status string, pid int) string {
	if status == StatusRunning && !pidAlive(pid) {
		return StatusStale
	}
	return status
}

func entryOf(a *Action) IndexEntry {
	s := ""
	switch {
	case a.Output != nil:
		s = a.Output.Summary
	case a.Error != nil:
		s = a.Error.Message
	}
	return IndexEntry{ID: a.ID, Type: a.Type, Status: a.Status, StartedAt: a.StartedAt,
		Summary: s, ParentID: a.ParentID, DerivedFrom: a.DerivedFrom, Pid: a.Pid}
}

func (idx *Index) upsert(e IndexEntry) {
	for i := range idx.Actions {
		if idx.Actions[i].ID == e.ID {
			idx.Actions[i] = e
			return
		}
	}
	idx.Actions = append(idx.Actions, e)
	sort.Slice(idx.Actions, func(i, j int) bool { return idx.Actions[i].ID < idx.Actions[j].ID })
}

// loadIndex 读取索引；缺失或损坏时从 actions/ 重建（仅内存）。
func (r *Recorder) loadIndex() *Index {
	var idx Index
	if err := workspace.ReadJSON(r.WS.IndexPath(), &idx); err == nil {
		return &idx
	}
	return r.rebuild()
}

func (r *Recorder) rebuild() *Index {
	idx := &Index{WorkspaceID: r.WS.Info.WorkspaceID, CreatedAt: r.WS.Info.CreatedAt, Actions: []IndexEntry{}}
	all, _ := r.All()
	for _, a := range all {
		raw, err := r.readRaw(a.ID)
		if err == nil {
			idx.upsert(entryOf(raw))
		}
	}
	return idx
}

// Reindex 从 actions/ 重建并写入 index.json，返回条目数。
func (r *Recorder) Reindex() (int, error) {
	unlock, err := r.WS.Lock()
	if err != nil {
		return 0, err
	}
	defer unlock()
	idx := r.rebuild()
	return len(idx.Actions), workspace.WriteJSONAtomic(r.WS.IndexPath(), idx)
}
