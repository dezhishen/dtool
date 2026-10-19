package action

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dezhishen/dtool/pkg/types"
)

// ResultFile 是结果集里的一个产物文件；结果集管理只读取元信息。
type ResultFile struct {
	Role     string `json:"role"`
	Path     string `json:"path"`
	AbsPath  string `json:"abs_path"`
	MIME     string `json:"mime"`
	Bytes    int64  `json:"bytes"`
	SHA256   string `json:"sha256,omitempty"`
	Exists   bool   `json:"exists"`
	External bool   `json:"external,omitempty"`
}

// ResultSet 是一个 Action 产出的结果集（一个 Action 可能带多个文件，如 result.json + xlsx）。
type ResultSet struct {
	ActionID    string       `json:"action_id"`
	Type        string       `json:"type"`
	Status      string       `json:"status"`
	StartedAt   time.Time    `json:"started_at"`
	Summary     string       `json:"summary,omitempty"`
	RowCount    int          `json:"row_count,omitempty"`
	Columns     []string     `json:"columns,omitempty"`
	Bytes       int64        `json:"bytes"`
	Files       []ResultFile `json:"files"`
	ParentID    string       `json:"parent_id,omitempty"`
	DerivedFrom string       `json:"derived_from,omitempty"`
}

// ResultQuery 控制结果集收集范围。Type 为空或 all 表示 query + visualize；
// convert 产物属于 datasets，不纳入结果集。
type ResultQuery struct {
	Type   string
	Status string
	Hash   bool
}

func isResultType(typ, filter string) bool {
	switch filter {
	case "", "all":
		return typ == "query" || typ == "visualize"
	default:
		return typ == filter
	}
}

// CollectResults 从 Action 的 output.files 枚举结果集，不扫描整个磁盘。
func (r *Recorder) CollectResults(q ResultQuery) ([]ResultSet, error) {
	all, err := r.All()
	if err != nil {
		return nil, err
	}
	return r.collectResults(all, q), nil
}

func (r *Recorder) collectResults(all []*Action, q ResultQuery) []ResultSet {
	out := make([]ResultSet, 0, len(all))
	for _, a := range all {
		if !isResultType(a.Type, q.Type) {
			continue
		}
		if q.Status != "" && a.Status != q.Status {
			continue
		}
		if a.Output == nil || len(a.Output.Files) == 0 {
			continue
		}
		rs := ResultSet{
			ActionID: a.ID, Type: a.Type, Status: a.Status, StartedAt: a.StartedAt,
			Summary: a.Output.Summary, RowCount: a.Output.RowCount, Columns: a.Output.Columns,
			ParentID: a.ParentID, DerivedFrom: a.DerivedFrom,
		}
		for i, f := range a.Output.Files {
			rf := r.resultFile(a.Type, i, f, q.Hash)
			rs.Files = append(rs.Files, rf)
			rs.Bytes += rf.Bytes
		}
		out = append(out, rs)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].ActionID > out[j].ActionID
		}
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out
}

func (r *Recorder) resultFile(typ string, index int, path string, hash bool) ResultFile {
	abs := r.WS.Abs(path)
	rf := ResultFile{
		Role: resultRole(typ, index, path), Path: path, AbsPath: abs,
		MIME: types.MimeOf(path), External: !isUnder(abs, r.WS.OutputsDir()),
	}
	fi, err := os.Stat(abs)
	if err != nil || fi.IsDir() {
		return rf
	}
	rf.Exists = true
	rf.Bytes = fi.Size()
	if hash {
		rf.SHA256 = fileSHA256(abs)
	}
	return rf
}

func resultRole(typ string, index int, path string) string {
	if typ == "query" {
		if index == 0 {
			return "result"
		}
		return "output"
	}
	if typ == "visualize" {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".png", ".svg":
			return "chart"
		case ".xlsx", ".csv", ".md":
			return "table"
		}
	}
	return "artifact"
}

func fileSHA256(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ResultTypeStat 是按类型聚合的结果。
type ResultTypeStat struct {
	Sets     int   `json:"sets"`
	Files    int   `json:"files"`
	Bytes    int64 `json:"bytes"`
	Rows     int   `json:"rows"`
	Missing  int   `json:"missing"`
	External int   `json:"external"`
}

// ResultStats 是结果集统计；只含元数据，不含具体值。
type ResultStats struct {
	Sets     int                       `json:"sets"`
	Files    int                       `json:"files"`
	Bytes    int64                     `json:"bytes"`
	Rows     int                       `json:"rows"`
	Missing  int                       `json:"missing"`
	External int                       `json:"external"`
	ByType   map[string]ResultTypeStat `json:"by_type"`
	ByAge    map[string]int            `json:"by_age"`
	Oldest   *time.Time                `json:"oldest,omitempty"`
	Newest   *time.Time                `json:"newest,omitempty"`
}

func ResultStatistics(sets []ResultSet) ResultStats {
	st := ResultStats{ByType: map[string]ResultTypeStat{}, ByAge: map[string]int{}}
	now := time.Now().UTC()
	bumpAge := func(t time.Time) {
		switch d := now.Sub(t); {
		case d <= 7*24*time.Hour:
			st.ByAge["<=7d"]++
		case d <= 30*24*time.Hour:
			st.ByAge["8-30d"]++
		case d <= 90*24*time.Hour:
			st.ByAge["31-90d"]++
		default:
			st.ByAge[">90d"]++
		}
	}
	for _, s := range sets {
		ts := st.ByType[s.Type]
		ts.Sets++
		ts.Rows += s.RowCount
		st.Sets++
		st.Rows += s.RowCount
		if st.Oldest == nil || s.StartedAt.Before(*st.Oldest) {
			t := s.StartedAt
			st.Oldest = &t
		}
		if st.Newest == nil || s.StartedAt.After(*st.Newest) {
			t := s.StartedAt
			st.Newest = &t
		}
		bumpAge(s.StartedAt)
		for _, f := range s.Files {
			ts.Files++
			ts.Bytes += f.Bytes
			if !f.Exists {
				ts.Missing++
				st.Missing++
			}
			if f.External {
				ts.External++
				st.External++
			}
			st.Files++
			st.Bytes += f.Bytes
		}
		st.ByType[s.Type] = ts
	}
	return st
}

// VerifyReport 是结果文件的存在性/校验和报告。
type VerifyReport struct {
	OK       bool        `json:"ok"`
	Sets     int         `json:"sets"`
	Files    int         `json:"files"`
	Missing  int         `json:"missing"`
	Results  []ResultSet `json:"results"`
	Problems []string    `json:"problems,omitempty"`
}

func VerifyResults(sets []ResultSet) VerifyReport {
	rep := VerifyReport{Sets: len(sets), Results: sets}
	for _, s := range sets {
		for _, f := range s.Files {
			rep.Files++
			if !f.Exists {
				rep.Missing++
				rep.Problems = append(rep.Problems, "missing: "+f.Path)
			} else if f.Bytes == 0 {
				rep.Problems = append(rep.Problems, "empty file: "+f.Path)
			}
		}
	}
	rep.OK = len(rep.Problems) == 0
	return rep
}

// GCOptions 控制结果集清理；默认只计划不删除（Apply=false）。
type GCOptions struct {
	Type      string
	Status    string
	OlderThan time.Duration
	KeepLast  int
	Apply     bool
	Force     bool
}

type GCFile struct {
	ActionID string `json:"action_id"`
	Type     string `json:"type"`
	Path     string `json:"path"`
	AbsPath  string `json:"abs_path"`
	Bytes    int64  `json:"bytes"`
}

type GCSkip struct {
	ActionID string `json:"action_id,omitempty"`
	Path     string `json:"path,omitempty"`
	Reason   string `json:"reason"`
}

// GCReport 是清理计划/结果。
type GCReport struct {
	DryRun      bool      `json:"dry_run"`
	Cutoff      time.Time `json:"cutoff"`
	Kept        int       `json:"kept"`
	Deleted     []GCFile  `json:"deleted,omitempty"`
	WouldDelete []GCFile  `json:"would_delete,omitempty"`
	Skipped     []GCSkip  `json:"skipped,omitempty"`
	Bytes       int64     `json:"bytes"`
	Problems    []string  `json:"problems,omitempty"`
}

// GCResults 只删除工作区 outputs/ 下的结果文件；外部 --output、数据集、
// 仍被其他 Action 引用或最近 KeepLast 个结果默认不动。
func (r *Recorder) GCResults(o GCOptions) (*GCReport, error) {
	all, err := r.All()
	if err != nil {
		return nil, err
	}
	sets := r.collectResults(all, ResultQuery{Type: o.Type, Status: o.Status})
	cutoff := time.Now().UTC().Add(-o.OlderThan)
	rep := &GCReport{DryRun: !o.Apply, Cutoff: cutoff}

	keep := map[string]bool{}
	perType := map[string]int{}
	for _, s := range sets { // 已按时间倒序
		if perType[s.Type] < o.KeepLast {
			keep[s.ActionID] = true
			rep.Kept++
			perType[s.Type]++
		}
	}

	refAction := map[string]bool{}
	refFile := map[string]map[string]bool{}
	for _, a := range all {
		if a.Output != nil {
			for _, f := range a.Output.Files {
				abs := r.WS.Abs(f)
				if refFile[abs] == nil {
					refFile[abs] = map[string]bool{}
				}
				refFile[abs][a.ID] = true
			}
		}
		if a.DerivedFrom != "" {
			refAction[a.DerivedFrom] = true
		}
		scanStrings(a.Input, func(s string) {
			if strings.HasPrefix(s, "action:") {
				refAction[strings.TrimPrefix(s, "action:")] = true
			}
			if strings.ContainsAny(s, `/\`) || strings.HasPrefix(s, ".dtool/") {
				if abs := r.WS.Abs(s); fileExists(abs) {
					if refFile[abs] == nil {
						refFile[abs] = map[string]bool{}
					}
					refFile[abs][a.ID] = true
				}
			}
		})
	}

	for _, s := range sets {
		if keep[s.ActionID] {
			rep.Skipped = append(rep.Skipped, GCSkip{ActionID: s.ActionID, Reason: "keep-last"})
			continue
		}
		if !o.Force && refAction[s.ActionID] {
			rep.Skipped = append(rep.Skipped, GCSkip{ActionID: s.ActionID, Reason: "referenced by another action"})
			continue
		}
		if !s.StartedAt.Before(cutoff) {
			rep.Skipped = append(rep.Skipped, GCSkip{ActionID: s.ActionID, Reason: "newer than --older-than"})
			continue
		}
		for _, f := range s.Files {
			if f.External {
				rep.Skipped = append(rep.Skipped, GCSkip{ActionID: s.ActionID, Path: f.Path, Reason: "external --output"})
				continue
			}
			if !f.Exists {
				rep.Skipped = append(rep.Skipped, GCSkip{ActionID: s.ActionID, Path: f.Path, Reason: "missing"})
				continue
			}
			if !o.Force && referencedByOther(refFile[f.AbsPath], s.ActionID) {
				rep.Skipped = append(rep.Skipped, GCSkip{ActionID: s.ActionID, Path: f.Path, Reason: "file referenced by another action"})
				continue
			}
			item := GCFile{ActionID: s.ActionID, Type: s.Type, Path: f.Path, AbsPath: f.AbsPath, Bytes: f.Bytes}
			if !o.Apply {
				rep.WouldDelete = append(rep.WouldDelete, item)
				rep.Bytes += f.Bytes
				continue
			}
			if err := os.Remove(f.AbsPath); err != nil {
				rep.Problems = append(rep.Problems, "delete "+f.Path+": "+err.Error())
				continue
			}
			removeEmptyParents(f.AbsPath, r.WS.OutputsDir())
			rep.Deleted = append(rep.Deleted, item)
			rep.Bytes += f.Bytes
		}
	}
	return rep, nil
}

func referencedByOther(refs map[string]bool, owner string) bool {
	for id := range refs {
		if id != owner {
			return true
		}
	}
	return false
}

func scanStrings(v any, fn func(string)) {
	switch x := v.(type) {
	case string:
		fn(x)
	case []any:
		for _, e := range x {
			scanStrings(e, fn)
		}
	case map[string]any:
		for _, e := range x {
			scanStrings(e, fn)
		}
	}
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

func isUnder(abs, root string) bool {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func removeEmptyParents(file, root string) {
	dir := filepath.Dir(file)
	for isUnder(dir, root) && dir != root {
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}
