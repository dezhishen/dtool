package visualize

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/golang/freetype/truetype"
	"github.com/wcharczuk/go-chart/v2/roboto"
)

// makeTTC 把单个 TTF 包装成只含一个字体的 TTC（表偏移整体后移 16 字节）。
func makeTTC(ttf []byte) []byte {
	be := binary.BigEndian
	out := make([]byte, 16, 16+len(ttf))
	copy(out, "ttcf")
	be.PutUint32(out[4:], 0x00010000)
	be.PutUint32(out[8:], 1)
	be.PutUint32(out[12:], 16)
	out = append(out, ttf...)
	num := int(be.Uint16(out[16+4:]))
	for i := 0; i < num; i++ {
		p := 16 + 12 + 16*i + 8
		be.PutUint32(out[p:], be.Uint32(out[p:])+16)
	}
	return out
}

// stubCoverage 让测试用的 Roboto 副本被视为"中文字体"：长度恰为 Roboto 的才含拉丁字形。
func stubCoverage(t *testing.T) {
	t.Helper()
	old := fontCoverage
	fontCoverage = func(data []byte) (bool, bool) { return true, len(data) == len(roboto.Roboto) }
	t.Cleanup(func() { fontCoverage = old })
}

// withPad 返回仍可解析、但被 stubCoverage 视为"仅 CJK、无拉丁"的字体副本。
func withPad(ttf []byte) []byte { return append(append([]byte(nil), ttf...), 0) }

func write(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtractTTC(t *testing.T) {
	ttc := makeTTC(roboto.Roboto)
	if _, err := truetype.Parse(ttc); err == nil {
		t.Log("note: parser accepted raw TTC")
	}
	got, err := extractTTC(ttc, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := truetype.Parse(got); err != nil {
		t.Fatalf("extracted font not parseable: %v", err)
	}
	if _, err := extractTTC(ttc, 1); err == nil {
		t.Fatal("out-of-range index accepted")
	}
	if _, err := extractTTC(ttc[:40], 0); err == nil {
		t.Fatal("truncated ttc accepted")
	}
	same, err := extractTTC(roboto.Roboto, 0)
	if err != nil || len(same) != len(roboto.Roboto) {
		t.Fatal("plain ttf must pass through untouched")
	}
}

func TestLoadFontFile(t *testing.T) {
	dir := t.TempDir()
	ttf := write(t, dir, "a.ttf", roboto.Roboto)
	ttc := write(t, dir, "b.ttc", makeTTC(roboto.Roboto))
	junk := write(t, dir, "c.ttf", []byte("not a font"))
	for _, p := range []string{ttf, ttc} {
		if _, err := LoadFontFile(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if _, err := LoadFontFile(junk); err == nil {
		t.Error("junk accepted")
	}
	if _, err := LoadFontFile(filepath.Join(dir, "missing.ttf")); err == nil {
		t.Error("missing file accepted")
	}
}

func TestFindSystemFontPrefersRankAndSkipsUnusable(t *testing.T) {
	stubCoverage(t)
	dir := t.TempDir()
	write(t, dir, "sub/NotoSansCJK-Regular.ttc", []byte("CFF-like, unparseable")) // 优先级最高但不可用
	write(t, dir, "unrelated.ttf", roboto.Roboto)                                 // 不是中文字体
	want := write(t, dir, "wqy-microhei.ttc", makeTTC(roboto.Roboto))
	write(t, dir, "SimSun.ttf", roboto.Roboto) // 优先级更低

	path, data, ok := FindSystemFont([]string{dir, filepath.Join(dir, "no-such-dir")})
	if !ok || path != want || len(data) == 0 {
		t.Fatalf("path=%q ok=%v", path, ok)
	}
	if _, _, ok := FindSystemFont([]string{t.TempDir()}); ok {
		t.Fatal("found a font in an empty dir")
	}
}

func TestResolveFontPrecedence(t *testing.T) {
	stubCoverage(t)
	sys := t.TempDir()
	sysFont := write(t, sys, "msyh.ttf", roboto.Roboto)
	explicit := write(t, t.TempDir(), "mine.ttf", roboto.Roboto)
	env := write(t, t.TempDir(), "env.ttf", roboto.Roboto)

	t.Setenv("DTOOL_FONT", env)
	if src, _, err := ResolveFont(explicit, []string{sys}); err != nil || src != explicit {
		t.Fatalf("explicit should win: %q %v", src, err)
	}
	if src, _, err := ResolveFont("", []string{sys}); err != nil || src != env {
		t.Fatalf("env should beat system: %q %v", src, err)
	}
	t.Setenv("DTOOL_FONT", "")
	if src, data, err := ResolveFont("", []string{sys}); err != nil || src != sysFont || data == nil {
		t.Fatalf("system fallback: %q %v", src, err)
	}
	if src, data, err := ResolveFont("", []string{t.TempDir()}); err != nil || src != "" || data != nil {
		t.Fatalf("nothing found should be a soft miss: %q %v", src, err)
	}
	if _, _, err := ResolveFont(filepath.Join(sys, "missing.ttf"), nil); err == nil {
		t.Fatal("missing explicit font must be an error")
	}
	t.Setenv("DTOOL_FONT", filepath.Join(sys, "missing.ttf"))
	if _, _, err := ResolveFont("", []string{sys}); err == nil {
		t.Fatal("broken DTOOL_FONT must be an error")
	}
}

func TestSystemFontDirsNotEmpty(t *testing.T) {
	if len(SystemFontDirs()) == 0 {
		t.Fatal("no dirs")
	}
}

// 本机装有中文字体时，自动发现应能找到可解析的字体；没有则跳过。
func TestRealSystemFontIfPresent(t *testing.T) {
	path, data, ok := FindSystemFont(SystemFontDirs())
	if !ok {
		t.Skip("no usable CJK system font on this machine")
	}
	if _, err := truetype.Parse(data); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	t.Logf("system CJK font: %s", path)
}

func TestCoverage(t *testing.T) {
	cjk, latin := coverage(roboto.Roboto)
	if cjk || !latin {
		t.Fatalf("roboto: cjk=%v latin=%v", cjk, latin)
	}
	if cjk, latin = coverage([]byte("junk")); cjk || latin {
		t.Fatal("junk reported coverage")
	}
}

func TestFindSystemFontPrefersFullCoverageOverCJKOnly(t *testing.T) {
	stubCoverage(t)
	dir := t.TempDir()
	write(t, dir, "DroidSansFallback.ttf", withPad(roboto.Roboto)) // 排名更高，但无数字字形
	full := write(t, dir, "simsun.ttf", roboto.Roboto)
	if path, _, ok := FindSystemFont([]string{dir}); !ok || path != full {
		t.Fatalf("got %q, want %q", path, full)
	}

	only := t.TempDir()
	fb := write(t, only, "DroidSansFallback.ttf", withPad(roboto.Roboto))
	if path, _, ok := FindSystemFont([]string{only}); !ok || path != fb {
		t.Fatalf("CJK-only font should be the fallback: %q", path)
	}
}

func TestFontWithoutLatinWarns(t *testing.T) {
	stubCoverage(t)
	cols, r := rows(t, `[{"区域":"北","销量":3}]`)
	sys := t.TempDir()
	write(t, sys, "DroidSansFallback.ttf", withPad(roboto.Roboto))
	t.Setenv("DTOOL_FONT", "")
	res, err := Render(cols, r, Options{Type: "bar", X: "区域", Y: "销量", Format: "svg",
		OutFile: filepath.Join(t.TempDir(), "o.svg"), FontDirs: []string{sys}})
	if err != nil || len(res.Warnings) != 1 || res.Font == "" {
		t.Fatalf("%+v %v", res, err)
	}
}
