package config

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dezhishen/dtool/pkg/types"
	"gopkg.in/yaml.v3"
)

// Config 对应 -c config.yaml；命令行参数优先于配置文件。
type Config struct {
	Font        string `yaml:"font"`         // TTF/TTC 字体路径，用于图表中文
	Workspace   string `yaml:"workspace"`    // 工作区目录
	PreviewRows int    `yaml:"preview_rows"` // Action 预览行数
}

// Load 读取配置；font/workspace 中的相对路径相对配置文件所在目录解析，支持 ~/ 前缀。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, types.Errorf(types.CodeNotFound, "config not found: %s", path)
		}
		return nil, types.Errorf(types.CodeGeneral, "read config: %v", err)
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, types.Errorf(types.CodeUsage, "invalid config %s: %v", path, err)
	}
	base := filepath.Dir(path)
	c.Font = resolve(base, c.Font)
	c.Workspace = resolve(base, c.Workspace)
	return &c, nil
}

func resolve(base, p string) string {
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(base, p)
}
