package dataset

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/dezhishen/dtool/internal/workspace"
	"github.com/dezhishen/dtool/pkg/types"
)

const (
	DataFile   = "data.json"
	SchemaFile = "data.schema.json"
	MetaFile   = "dataset.json"
)

// Meta 描述一个独立于 Action 的数据集；同名重新转换会覆盖数据并刷新 UpdatedAt。
type Meta struct {
	Name        string    `json:"name"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	ActionID    string    `json:"action_id,omitempty"` // 产出当前版本的 Action
	Source      string    `json:"source,omitempty"`
	Sheet       string    `json:"sheet,omitempty"`
	RecordCount int       `json:"record_count"`
	Columns     []string  `json:"columns"`
}

// Info 在 Meta 之上附带相对项目目录的文件路径。
type Info struct {
	Meta
	DataFile   string `json:"data_file"`
	SchemaFile string `json:"schema_file"`
}

type Store struct {
	WS *workspace.Workspace
}

func (s *Store) Dir(name string) string {
	return filepath.Join(s.WS.DatasetsDir(), workspace.SafeName(name))
}
func (s *Store) Path(name string) string       { return filepath.Join(s.Dir(name), DataFile) }
func (s *Store) SchemaPath(name string) string { return filepath.Join(s.Dir(name), SchemaFile) }

func notFound(name string) error {
	return types.Errorf(types.CodeNotFound, "dataset not found: %s", name).
		WithHint("用 `dtool datasets list` 查看已有数据集")
}

func (s *Store) Get(name string) (*Meta, error) {
	var m Meta
	if err := workspace.ReadJSON(filepath.Join(s.Dir(name), MetaFile), &m); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, notFound(name)
		}
		return nil, err
	}
	return &m, nil
}

func (s *Store) info(m Meta) Info {
	return Info{Meta: m, DataFile: s.WS.Rel(s.Path(m.Name)), SchemaFile: s.WS.Rel(s.SchemaPath(m.Name))}
}

func (s *Store) Info(name string) (*Info, error) {
	m, err := s.Get(name)
	if err != nil {
		return nil, err
	}
	i := s.info(*m)
	return &i, nil
}

// List 按名称排序列出所有数据集。
func (s *Store) List() ([]Info, error) {
	entries, err := os.ReadDir(s.WS.DatasetsDir())
	if err != nil {
		return nil, err
	}
	out := []Info{}
	for _, e := range entries {
		if !e.IsDir() || e.Name()[0] == '.' {
			continue
		}
		if m, err := s.Get(e.Name()); err == nil {
			out = append(out, s.info(*m))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Lookup 供 SQL 直接用数据集名作表名：存在则返回数据文件路径。
func (s *Store) Lookup(name string) (string, bool) {
	if _, err := s.Get(name); err != nil {
		return "", false
	}
	return s.Path(name), true
}

// NewStaging 在数据集目录内创建暂存目录（同一文件系统，便于原子 rename）。
func (s *Store) NewStaging() (string, error) {
	return os.MkdirTemp(s.WS.DatasetsDir(), ".staging-*")
}

// Commit 把暂存目录中的 data.json 与 data.schema.json 提交为数据集 m.Name，并更新元数据。
func (s *Store) Commit(stage string, m Meta) (*Meta, error) {
	m.Name = workspace.SafeName(m.Name)
	for _, f := range []string{DataFile, SchemaFile} {
		if _, err := os.Stat(filepath.Join(stage, f)); err != nil {
			return nil, fmt.Errorf("staging is missing %s: 数据集必须同时包含数据与 Schema", f)
		}
	}
	unlock, err := s.WS.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	now := time.Now().UTC()
	m.CreatedAt = now
	if m.UpdatedAt.IsZero() {
		m.UpdatedAt = now
	}
	if old, err := s.Get(m.Name); err == nil {
		m.CreatedAt = old.CreatedAt
	}
	dir := s.Dir(m.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	for _, f := range []string{DataFile, SchemaFile} {
		if err := os.Rename(filepath.Join(stage, f), filepath.Join(dir, f)); err != nil {
			return nil, err
		}
	}
	if err := workspace.WriteJSONAtomic(filepath.Join(dir, MetaFile), m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *Store) Delete(name string) error {
	if _, err := s.Get(name); err != nil {
		return err
	}
	unlock, err := s.WS.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	return os.RemoveAll(s.Dir(name))
}
