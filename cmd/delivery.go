package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"

	"github.com/dezhishen/dtool/pkg/types"
	"github.com/spf13/cobra"
)

// forbidStrong 在强隐私模式下拒绝“把具体数据带到 stdout”的动作。
// 唯一出口是用户本人修改配置文件里的 privacy。
func forbidStrong(what string) error {
	if !strongPrivacy() {
		return nil
	}
	return types.Errorf(types.CodeForbidden, "强隐私模式禁止%s", what).
		WithHint("结果已落盘；请把产物文件发给用户。确需读取具体值，只能由用户本人把配置文件的 privacy 改为 normal")
}

func artifactOf(role, path string) (types.Artifact, error) {
	resolved := path
	if !filepath.IsAbs(resolved) {
		if _, err := os.Stat(resolved); err != nil {
			// 产物路径是相对工作区项目目录的（见 workspace.Rel），cwd 未必等于它。
			resolved = filepath.Join(filepath.Dir(g.workspace), resolved)
		}
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return types.Artifact{}, types.Errorf(types.CodeGeneral, "resolve artifact %s: %v", path, err)
	}
	f, err := os.Open(resolved)
	if err != nil {
		return types.Artifact{}, types.Errorf(types.CodeNotFound, "artifact missing: %s", path)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return types.Artifact{}, types.Errorf(types.CodeGeneral, "read artifact %s: %v", path, err)
	}
	return types.Artifact{
		Role: role, Path: path, AbsPath: abs, MIME: mimeOf(path),
		Bytes: n, SHA256: hex.EncodeToString(h.Sum(nil)),
	}, nil
}

func mimeOf(path string) string {
	switch filepath.Ext(path) {
	case ".json":
		return "application/json"
	case ".csv":
		return "text/csv"
	case ".md":
		return "text/markdown"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".png":
		return "image/png"
	case ".svg":
		return "image/svg+xml"
	case ".txt":
		return "text/plain"
	default:
		return "application/octet-stream"
	}
}

// deliveryOf 只读取文件元信息，不把内容写进 stdout。
type artifactItem struct{ Role, Path string }

func deliveryOf(actionID string, items ...artifactItem) (types.Delivery, error) {
	d := types.Delivery{
		Success: true, ActionID: actionID,
		Send: types.SendHint{
			Mode: "attachment",
			Hint: "把 artifacts[].abs_path 作为文件附件发给用户；不要读取或复述其内容",
		},
	}
	for _, it := range items {
		a, err := artifactOf(it.Role, it.Path)
		if err != nil {
			return types.Delivery{}, err
		}
		d.Artifacts = append(d.Artifacts, a)
	}
	return d, nil
}

func addArtifact(items *[]artifactItem, role, path string) {
	if path == "" {
		return
	}
	*items = append(*items, artifactItem{role, path})
}

func printDelivery(c *cobra.Command, actionID string, items ...artifactItem) error {
	d, err := deliveryOf(actionID, items...)
	if err != nil {
		return err
	}
	return printJSON(c.OutOrStdout(), d)
}
