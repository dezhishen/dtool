package memguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 阶梯：full → stream+memory → stream+disk（倍率取实测量级）
func rungs() []Candidate {
	return []Candidate{
		{Name: "full+memory", Factor: 13},
		{Name: "stream+memory", Factor: 2},
		{Name: "stream+disk", Factor: 0.3},
	}
}

const mib = 1 << 20

func TestChoosePicksFastestThatFits(t *testing.T) {
	// 预算宽松：13×100MB = 1300MB ≤ 2000MB，直接用最快的档
	p, err := Choose(ChooseRequest{Size: 100 * mib, Memory: Memory{Available: 2000 * mib, Hard: true}, Candidates: rungs()})
	if err != nil {
		t.Fatal(err)
	}
	if p.Chosen.Name != "full+memory" || p.Forced || p.Risky {
		t.Fatalf("应选最快档：%+v", p)
	}
	if p.Threshold != 2000*mib*hardExtraPercent/100 {
		t.Fatalf("硬上限阈值应打八折：%d", p.Threshold)
	}
}

func TestChooseSoftBudgetKeepsFullAvailable(t *testing.T) {
	// 软预算不打折：同样是 2000MB，阈值就是 2000MB
	p, _ := Choose(ChooseRequest{Size: 100 * mib, Memory: Memory{Available: 2000 * mib}, Candidates: rungs()})
	if p.Threshold != 2000*mib {
		t.Fatalf("软预算阈值 = %d, want %d", p.Threshold, 2000*mib)
	}
	// 1500MB 输入 × 13 = 19500MB 放不下→ 2×1500 = 3000MB 也放不下 → 0.3× = 450MB 放得下
	p, _ = Choose(ChooseRequest{Size: 1500 * mib, Memory: Memory{Available: 2000 * mib}, Candidates: rungs()})
	if p.Chosen.Name != "stream+disk" {
		t.Fatalf("应降到磁盘档：%+v", p)
	}
	if len(p.Rejected) != 2 || !strings.Contains(p.Rejected[0].Why, "成功率") {
		t.Fatalf("被跳过的档要说明原因：%+v", p.Rejected)
	}
}

// 擦边：预计放不下，但历史记录显示「实际峰值只有预计的七成」，于是值得一试。
func TestChooseTriesBorderlineWhenHistorySaysCheap(t *testing.T) {
	var samples []Sample
	for i := 0; i < 10; i++ {
		samples = append(samples, Sample{Rung: "stream+memory", Size: 100 * mib,
			Predicted: 200 * mib, Peak: 140 * mib, OK: true, At: time.Now()})
	}
	// 软预算 160MB（阈值就是 160MB）：预计 200MB 放不下，但历史 ratio ≈ 0.7 →
	// 阈值/预计 = 0.8 > 0.7，实际大概率过得去，值得一试。
	p, err := Choose(ChooseRequest{Size: 100 * mib, Memory: Memory{Available: 160 * mib},
		Candidates: rungs(), Samples: samples})
	if err != nil {
		t.Fatal(err)
	}
	if p.Chosen.Name != "stream+memory" {
		t.Fatalf("历史显示有机会时应试快档：%+v", p)
	}
	if !strings.Contains(p.Reason, "值得一试") {
		t.Fatalf("理由要写清是擦边：%q", p.Reason)
	}
}

// 没有历史 + 预计明显放不下：不要浪费一次尝试，直接降档。
func TestChooseSkipsHopelessRungWithoutHistory(t *testing.T) {
	p, _ := Choose(ChooseRequest{Size: 100 * mib, Memory: Memory{Available: 160 * mib, Hard: true}, Candidates: rungs()})
	if p.Chosen.Name == "full+memory" {
		t.Fatalf("明显放不下的档不该试：%+v", p)
	}
}

// 所有档都放不下：try 策略仍试最省档（失败留给 Action 记录，AI 据此重试），
// strict 策略交给调用方失败。
func TestChooseWhenNothingFits(t *testing.T) {
	req := ChooseRequest{Size: 2000 * mib, Memory: Memory{Available: 160 * mib, Hard: true}, Candidates: rungs()}
	p, err := Choose(req)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Risky || p.Chosen.Name != "stream+disk" {
		t.Fatalf("try 策略应仍试最省档并标记 risky：%+v", p)
	}
	if len(p.Rejected) != len(rungs()) {
		t.Fatalf("三档都放不下时都要记录原因：%+v", p.Rejected)
	}

	req.Policy = PolicyStrict
	p, _ = Choose(req)
	if !p.Risky || !strings.Contains(p.Reason, "strict") {
		t.Fatalf("strict 策略要说明不尝试：%+v", p)
	}
}

func TestChooseForcedWins(t *testing.T) {
	p, err := Choose(ChooseRequest{Size: 100 * mib, Memory: Memory{Available: 1 << 20, Hard: true},
		Candidates: rungs(), Forced: "full+memory"})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Forced || p.Chosen.Name != "full+memory" {
		t.Fatalf("操作者指定必须生效（哪怕预计会崩）：%+v", p)
	}
	if !strings.Contains(p.Reason, "操作者指定") {
		t.Fatalf("理由要写明是强制的：%q", p.Reason)
	}
	if _, err := Choose(ChooseRequest{Size: mib, Candidates: rungs(), Forced: "nope"}); err == nil {
		t.Fatal("未知档名应报错")
	}
}

// 预算未知：没有依据可比，直接用最快档（不假装知道）。
func TestChooseWithoutBudgetPicksFastest(t *testing.T) {
	p, _ := Choose(ChooseRequest{Size: 100 * mib, Candidates: rungs()})
	if p.Chosen.Name != "full+memory" || p.Threshold != 0 {
		t.Fatalf("%+v", p)
	}
}

func TestTryChanceMonotonicAndPrior(t *testing.T) {
	// 阈值与预计相同时：先验 N(0.85, 0.30) 给约 69%
	if p := TryChance(nil, "x", 100, 100); p < 0.6 || p > 0.8 {
		t.Fatalf("先验概率 = %.2f", p)
	}
	lo := TryChance(nil, "x", 100, 50)
	hi := TryChance(nil, "x", 100, 150)
	if !(lo < 0.5 && hi > 0.9) {
		t.Fatalf("应随富余单调上升：lo=%.2f hi=%.2f", lo, hi)
	}
	// 同档样本占比高时会明显偏离先验
	var s []Sample
	for i := 0; i < 20; i++ {
		s = append(s, Sample{Rung: "x", Predicted: 100, Peak: 40})
	}
	if p := TryChance(s, "x", 100, 100); p < 0.9 {
		t.Fatalf("实测 ratio=0.4 时 h=1 几乎必过，得到 %.2f", p)
	}
}

func TestHistoryRoundTripAndStats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plans", "samples.json")
	h, err := LoadHistory(path)
	if err != nil || len(h.Samples) != 0 {
		t.Fatalf("首次读取应为空：%v %+v", err, h)
	}
	h.Add(Sample{Rung: "stream+disk", Size: 123 * mib, Predicted: 37 * mib, Peak: 20 * mib, OK: true, MS: 12650})
	h.Add(Sample{Rung: "stream+memory", Size: 123 * mib, Predicted: 246 * mib, Peak: 176 * mib, OK: true, MS: 12540})
	if err := h.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("应写入 %s：%v", path, err)
	}
	got, err := LoadHistory(path)
	if err != nil || len(got.Samples) != 2 {
		t.Fatalf("回读：%v %+v", err, got)
	}
	st, ok := got.StatOf("stream+memory")
	if !ok || st.N != 1 || st.OKRate != 1 {
		t.Fatalf("统计：%+v", st)
	}
	if st.RatioP50 < 0.7 || st.RatioP50 > 0.72 {
		t.Fatalf("ratio 中位数 = %.3f（176/246 ≈ 0.715）", st.RatioP50)
	}
}

func TestHistoryCapsSamplesAndSurvivesCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "samples.json")
	h, _ := LoadHistory(path)
	for i := 0; i < maxSamples+20; i++ {
		h.Add(Sample{Rung: "r", Predicted: 1, Peak: 1, OK: true})
	}
	if len(h.Samples) != maxSamples {
		t.Fatalf("应裁剪到 %d 条：%d", maxSamples, len(h.Samples))
	}
	if err := os.WriteFile(path, []byte("{ 坏掉的 json"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadHistory(path)
	if err != nil || len(got.Samples) != 0 {
		t.Fatalf("坏文件应退化为空历史而不是报错：%v %+v", err, got)
	}
}

func TestPlanFileUnderWorkspace(t *testing.T) {
	if got := PlanFile(".dtool"); got != filepath.Join(".dtool", "plans", "samples.json") {
		t.Fatalf("PlanFile = %q", got)
	}
}
