package config

import (
	"bytes"
	"errors"
	"fmt"
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
	Engine      string  `yaml:"engine"`       // 查询后端：auto（默认 sqlite）/ 已注册引擎名
	DB          DB      `yaml:"db"`           // 查询后端配置：db.engine.<name>: {...}
	Privacy     Privacy `yaml:"privacy"`      // strong（默认）/ normal；只能由用户改配置文件设置
}

// DB 对应 db: 段；目前只有查询后端相关配置。
type DB struct {
	Engine EngineConfig `yaml:"engine"`
}

// EngineConfig 收集 db.engine 下的配置，按后端名分组：
//
//	db:
//	  engine:
//	    sqlite:
//	      cache_size_kb: 8192
//	    duckdb:
//	      threads: 4
//
// 同时接受两种简写：db.engine: sqlite（只选后端、不带配置）与
// db.engine.name: sqlite（在选择后端的同一条里保留分组配置）。
// 整棵 <name> 子树原样交给后端的 Configure，由后端自己解释键名。
type EngineConfig struct {
	// Name 是 db.engine 的简写选择，优先级高于顶层 engine:。
	Name string
	// Options 是「后端名（小写） -> db.engine.<name> 下的全部键值」。
	Options map[string]map[string]any
}

// For 返回某个后端的全部配置；没有配置时返回 nil。
func (e EngineConfig) For(name string) map[string]any {
	if e.Options == nil {
		return nil
	}
	return e.Options[strings.ToLower(strings.TrimSpace(name))]
}

// UnmarshalYAML 允许 db.engine 既是字符串（选后端）又是键值表（每个后端一组配置）。
// 因为键是后端名（未来还可能是任意插件名），这里不用 KnownFields，交给 Configure 校验。
func (e *EngineConfig) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var name string
		if err := node.Decode(&name); err != nil {
			return fmt.Errorf("db.engine: %w", err)
		}
		e.Name = strings.TrimSpace(name)
		return nil
	case yaml.MappingNode:
		opts := make(map[string]map[string]any, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := strings.ToLower(strings.TrimSpace(node.Content[i].Value))
			val := node.Content[i+1]
			if key == "name" {
				var name string
				if err := val.Decode(&name); err != nil {
					return fmt.Errorf("db.engine.name: %w", err)
				}
				e.Name = strings.TrimSpace(name)
				continue
			}
			if key == "" {
				return fmt.Errorf("db.engine: 后端名不能为空")
			}
			var m map[string]any
			if err := val.Decode(&m); err != nil {
				return fmt.Errorf("db.engine.%s 必须是键值表：%w", key, err)
			}
			opts[key] = m
		}
		e.Options = opts
		return nil
	}
	return fmt.Errorf("db.engine 必须是字符串（后端名）或键值表（后端名: 配置）")
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
