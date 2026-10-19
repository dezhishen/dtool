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

// Privacy 是隐私模式。默认强隐私：stdout 只给交付清单，不给任何数据。
// 只能由用户显式修改配置文件切换，命令行不提供覆盖入口。
type Privacy string

const (
	PrivacyStrong Privacy = "strong" // 默认：结果只落盘，stdout 只给文件位置/发送方式
	PrivacyNormal Privacy = "normal" // 普通：允许显式数据动作（--rows / datasets values）
)

// ParsePrivacy 解析配置里的 privacy；空值按 strong 处理。
func ParsePrivacy(s string) (Privacy, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(PrivacyStrong):
		return PrivacyStrong, nil
	case string(PrivacyNormal):
		return PrivacyNormal, nil
	default:
		return "", types.Errorf(types.CodeUsage, "invalid privacy %q", s).
			WithHint("只能是 strong（默认）或 normal；强隐私下请勿把该值暴露给自动化代理")
	}
}

// Config 对应 -c config.yaml；命令行参数优先于配置文件。
type Config struct {
	Font        string  `yaml:"font"`         // TTF/TTC 字体路径，用于图表中文
	Workspace   string  `yaml:"workspace"`    // 工作区目录
	PreviewRows int     `yaml:"preview_rows"` // Action 预览行数
	LoadMode    string  `yaml:"load_mode"`    // JSON 装入方式：auto / stream / full
	Store       string  `yaml:"store"`        // 落库位置：auto / memory / disk
	MemPolicy   string  `yaml:"mem_policy"`   // 都超预算时：try / strict
	Privacy     Privacy `yaml:"privacy"`      // strong（默认）/ normal；只能由用户改配置文件设置
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
