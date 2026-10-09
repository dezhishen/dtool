package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/oklog/ulid/v2"
)

type Info struct {
	WorkspaceID string    `json:"workspace_id"`
	CreatedAt   time.Time `json:"created_at"`
}

type Workspace struct {
	Root string
	Info Info
}

// Open 打开（必要时创建）工作区目录。
func Open(dir string) (*Workspace, error) {
	if dir == "" {
		dir = ".dtool"
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	w := &Workspace{Root: root}
	for _, d := range []string{w.ActionsDir(), w.OutputsDir(), w.DatasetsDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	p := filepath.Join(root, "workspace.json")
	if err := ReadJSON(p, &w.Info); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		w.Info = Info{WorkspaceID: "ws-" + ulid.Make().String(), CreatedAt: time.Now().UTC()}
		if err := WriteJSONAtomic(p, w.Info); err != nil {
			return nil, err
		}
	}
	return w, nil
}

func (w *Workspace) ActionsDir() string { return filepath.Join(w.Root, "actions") }
func (w *Workspace) OutputsDir() string { return filepath.Join(w.Root, "outputs") }

// DatasetsDir 存放独立于 Action 的数据集。
func (w *Workspace) DatasetsDir() string { return filepath.Join(w.Root, "datasets") }
func (w *Workspace) IndexPath() string   { return filepath.Join(w.Root, "index.json") }
func (w *Workspace) ProjectDir() string  { return filepath.Dir(w.Root) }

func (w *Workspace) ActionPath(id string) string {
	return filepath.Join(w.ActionsDir(), id+".json")
}

// ActionOutputDir 返回（创建）Action 产物目录：queries/<id>、charts/<id>，其余为 <type>/<id>。
// 数据集不在此列，见 DatasetsDir。
func (w *Workspace) ActionOutputDir(typ, id string) (string, error) {
	var d string
	switch typ {
	case "query":
		d = filepath.Join(w.OutputsDir(), "queries", id)
	case "visualize":
		d = filepath.Join(w.OutputsDir(), "charts", id)
	default:
		d = filepath.Join(w.OutputsDir(), typ, id)
	}
	return d, os.MkdirAll(d, 0o755)
}

// SafeName 把数据集名规整为可安全用作目录名的形式（保留字母、数字含中文、- _ .）。
func SafeName(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), ".")
	if r := []rune(out); len(r) > 64 {
		out = string(r[:64])
	}
	if out == "" {
		return "dataset"
	}
	return out
}

// Rel 把绝对路径转为相对项目目录（工作区的父目录）的正斜杠路径。
func (w *Workspace) Rel(p string) string {
	if !filepath.IsAbs(p) {
		return filepath.ToSlash(p)
	}
	r, err := filepath.Rel(w.ProjectDir(), p)
	if err != nil {
		return filepath.ToSlash(p)
	}
	return filepath.ToSlash(r)
}

func (w *Workspace) Abs(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(w.ProjectDir(), filepath.FromSlash(p))
}

// Lock 获取工作区写锁，返回释放函数。
func (w *Workspace) Lock() (func(), error) {
	p := filepath.Join(w.Root, ".lock")
	deadline := time.Now().Add(10 * time.Second)
	for {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			f.Close()
			return func() { os.Remove(p) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if st, e := os.Stat(p); e == nil && time.Since(st.ModTime()) > 30*time.Second {
			os.Remove(p)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("workspace locked: %s", p)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// WriteJSONAtomic 先写临时文件再 rename，避免读到半截文件。
func WriteJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

func ReadJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
