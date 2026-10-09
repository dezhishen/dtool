package visualize

import (
	"encoding/binary"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/golang/freetype/truetype"
)

// 按优先级排列的中文字体文件名关键字（已去掉空格/连字符/下划线并小写）。
var cjkFontHints = []string{
	"notosanscjk", "notosanssc", "sourcehansans", "wqymicrohei", "wqyzenhei", "wenquanyi",
	"msyh", "pingfang", "hiraginosansgb", "stheiti", "heiti", "simhei", "droidsansfallback",
	"notoserifcjk", "sourcehanserif", "uming", "ukai", "simsun", "mingliu", "arialuni",
}

// fontCoverage 可在测试中替换。
var fontCoverage = coverage

var fontExts = map[string]bool{".ttf": true, ".ttc": true, ".otf": true, ".otc": true}

// SystemFontDirs 返回当前平台的系统字体目录。
func SystemFontDirs() []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return []string{"/System/Library/Fonts", "/Library/Fonts", filepath.Join(home, "Library", "Fonts")}
	case "windows":
		win := os.Getenv("WINDIR")
		if win == "" {
			win = `C:\Windows`
		}
		return []string{filepath.Join(win, "Fonts")}
	}
	return []string{"/usr/share/fonts", "/usr/local/share/fonts",
		filepath.Join(home, ".fonts"), filepath.Join(home, ".local", "share", "fonts")}
}

// LoadFontFile 读取 TTF/TTC，并校验可被渲染器解析（CFF 轮廓的 OTF 不受支持）。
func LoadFontFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if data, err = extractTTC(data, 0); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if _, err := truetype.Parse(data); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return data, nil
}

// FindSystemFont 在 dirs 中按优先级寻找第一个可用的中文字体。
func FindSystemFont(dirs []string) (string, []byte, bool) {
	type cand struct {
		rank int
		path string
	}
	var cands []cand
	for _, d := range dirs {
		_ = filepath.WalkDir(d, func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() || !fontExts[strings.ToLower(filepath.Ext(p))] {
				return nil
			}
			name := strings.ToLower(strings.NewReplacer("-", "", "_", "", " ", "").Replace(filepath.Base(p)))
			for i, h := range cjkFontHints {
				if strings.Contains(name, h) {
					cands = append(cands, cand{i, p})
					break
				}
			}
			return nil
		})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].rank != cands[j].rank {
			return cands[i].rank < cands[j].rank
		}
		return cands[i].path < cands[j].path
	})
	// 优先选同时含中文与拉丁/数字字形的字体；只有纯 CJK 字体（如 Droid Sans Fallback）时作为兜底。
	var fbPath string
	var fbData []byte
	for _, c := range cands {
		data, err := LoadFontFile(c.path)
		if err != nil {
			continue
		}
		cjk, latin := fontCoverage(data)
		switch {
		case cjk && latin:
			return c.path, data, true
		case cjk && fbData == nil:
			fbPath, fbData = c.path, data
		}
	}
	return fbPath, fbData, fbData != nil
}

// coverage 报告字体是否含常用汉字与数字字形。
func coverage(data []byte) (cjk, latin bool) {
	f, err := truetype.Parse(data)
	if err != nil {
		return false, false
	}
	return f.Index('中') != 0, f.Index('0') != 0 && f.Index('A') != 0
}

// ResolveFont 优先级：显式路径（--font / 配置文件）> DTOOL_FONT > 系统字体。
// 显式路径加载失败直接报错；其余来源找不到返回空数据。
func ResolveFont(explicit string, dirs []string) (src string, data []byte, err error) {
	if explicit != "" {
		data, err = LoadFontFile(explicit)
		return explicit, data, err
	}
	if env := os.Getenv("DTOOL_FONT"); env != "" {
		data, err = LoadFontFile(env)
		return env, data, err
	}
	if dirs == nil {
		dirs = SystemFontDirs()
	}
	if p, d, ok := FindSystemFont(dirs); ok {
		return p, d, nil
	}
	return "", nil, nil
}

// extractTTC 从 TrueType Collection 中取出第 idx 个字体并重写为独立 sfnt；非 TTC 原样返回。
func extractTTC(data []byte, idx int) ([]byte, error) {
	if len(data) < 12 || string(data[:4]) != "ttcf" {
		return data, nil
	}
	be := binary.BigEndian
	n := int(be.Uint32(data[8:12]))
	if idx >= n || len(data) < 12+4*n {
		return nil, fmt.Errorf("invalid ttc: font index %d of %d", idx, n)
	}
	off := int(be.Uint32(data[12+4*idx:]))
	if off+12 > len(data) {
		return nil, fmt.Errorf("invalid ttc: bad offset")
	}
	num := int(be.Uint16(data[off+4:]))
	recs := off + 12
	if recs+16*num > len(data) {
		return nil, fmt.Errorf("invalid ttc: truncated table directory")
	}
	out := make([]byte, 12+16*num)
	copy(out, data[off:off+12])
	for i := 0; i < num; i++ {
		rec := data[recs+16*i : recs+16*i+16]
		tOff, tLen := int(be.Uint32(rec[8:12])), int(be.Uint32(rec[12:16]))
		if tOff+tLen > len(data) {
			return nil, fmt.Errorf("invalid ttc: table out of range")
		}
		copy(out[12+16*i:], rec)
		be.PutUint32(out[12+16*i+8:], uint32(len(out)))
		out = append(out, data[tOff:tOff+tLen]...)
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
	}
	return out, nil
}
