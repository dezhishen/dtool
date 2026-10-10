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

// Windows 小输入的真实情形：12MB 输入在内存档上实测到 183MB（≈15× 输入），
// 但按倍率估算只有 56MB。若历史不参与估算，这一档会被反复选中、反复撞看门狗——
// 试错永远不收敛。校准后应当改选更省的档。
func TestCalibrationFromHistoryConverges(t *testing.T) {
	// 阈值 139MB；40MB 输入下 full 预计 520MB（明显放不下，直接跳过），
	// stream+memory 预计 80MB（判「放得下」→ 被选中），实测却是 261MB（ratio 3.3）。
	hist := []Sample{{Rung: "stream+memory", Size: 40 << 20, Predicted: 80 << 20,
		Peak: 261 << 20, OK: false, At: time.Now()}}
	size := uint64(40 << 20)

	before, _ := Choose(ChooseRequest{Size: size, Memory: Memory{Available: 174 << 20}, Candidates: rungs()})
	if before.Chosen.Name != "stream+memory" {
		t.Fatalf("没有历史时按估算选中流式内存档：%+v", before.Chosen)
	}

	after, err := Choose(ChooseRequest{Size: size, Memory: Memory{Available: 174 << 20},
		Candidates: rungs(), Samples: hist})
	if err != nil {
		t.Fatal(err)
	}
	if after.Chosen.Name != "stream+disk" {
		t.Fatalf("历史显示实测超标后应降到磁盘档：%+v（%s）", after.Chosen, after.Reason)
	}
	// 被判「放不下」的原因是校准后的估算（80MB × 3.3 ≈ 261MB），不是原始估算
	var rj *Rejected
	for i := range after.Rejected {
		if after.Rejected[i].Name == "stream+memory" {
			rj = &after.Rejected[i]
		}
	}
	if rj == nil {
		t.Fatalf("应记录被跳过的流式内存档：%+v", after.Rejected)
	}
	if rj.Predicted < 260<<20 {
		t.Fatalf("校准后的预计峰值应明显高于原始 80MB：%s", HumanSize(rj.Predicted))
	}
}

// 校准只用同档样本：磁盘档的 ratio（0.85）不该把内存档的估算改小或改大。
func TestCalibrationIgnoresOtherRungs(t *testing.T) {
	hist := []Sample{{Rung: "stream+disk", Predicted: 100, Peak: 85, OK: true, At: time.Now()}}
	p, _ := Choose(ChooseRequest{Size: 100 << 20, Memory: Memory{Available: 4 << 30},
		Candidates: rungs(), Samples: hist})
	if p.Chosen.Name != "full+memory" {
		t.Fatalf("%+v", p.Chosen)
	}
	if p.Calibration != 1 || p.Predicted != p.RawPredicted {
		t.Fatalf("跨档样本不该参与校准：calib=%.2f n=%d", p.Calibration, p.CalibSamples)
	}
}

// 离谱的历史样本不能让估算无限膨胀（否则一档会被永久拉黑）。
func TestCalibrationIsCapped(t *testing.T) {
	hist := []Sample{{Rung: "stream+memory", Predicted: 1, Peak: 1000, OK: false, At: time.Now()}}
	p, _ := Choose(ChooseRequest{Size: 1 << 20, Memory: Memory{Available: 1 << 30},
		Candidates: rungs(), Samples: hist})
	if p.Calibration > maxCalibration {
		t.Fatalf("校准倍数应封顶 %.1f：%.2f", maxCalibration, p.Calibration)
	}
}

// 余量规则是**硬要求**：预计峰值没超阈值、但超了本档上限（阈值扣掉余量）时必须降档，
// 而且不能走「擦边值得一试」那条路——整块解析撞上硬上限是不可恢复的崩溃。
// 这条与文档「整块解析要求峰值 ≤ 预算 80%」严格对应。
func TestChooseRespectsHeadroomCeiling(t *testing.T) {
	rungs := []Candidate{
		{Name: "full+memory", Factor: 13, Base: 32 * mib, RequireHeadroomPercent: 20},
		{Name: "stream+memory", Factor: 2, Base: 32 * mib},
	}
	// 10MiB 输入：full 预计 32+130=162MiB；软预算 200MiB → 阈值 200MiB、上限 160MiB
	// → 162MiB 超上限（虽然 ≤ 阈值），必须降档
	p, err := Choose(ChooseRequest{Size: 10 * mib, Memory: Memory{Available: 200 * mib}, Candidates: rungs})
	if err != nil {
		t.Fatal(err)
	}
	if p.Chosen.Name != "stream+memory" {
		t.Fatalf("超出余量上限时应降档，实际选了 %s：%+v", p.Chosen.Name, p)
	}
	if len(p.Rejected) != 1 || !strings.Contains(p.Rejected[0].Why, "20% 余量") ||
		!strings.Contains(p.Rejected[0].Why, "不接受擦边") {
		t.Fatalf("被跳过的档要写明余量原因，且不接受擦边：%+v", p.Rejected)
	}
	// 预算再松一点（上限 168MiB ≥ 162MiB）就该用回最快的档
	p, _ = Choose(ChooseRequest{Size: 10 * mib, Memory: Memory{Available: 210 * mib}, Candidates: rungs})
	if p.Chosen.Name != "full+memory" {
		t.Fatalf("上限够时应选最快档，实际 %s：%+v", p.Chosen.Name, p)
	}
	if !strings.Contains(p.Reason, "20% 余量") {
		t.Fatalf("选中时也要说明该档要求余量：%q", p.Reason)
	}
}

// 输入体积上限：峰值放大不划算的档（整块解析 ×13）在输入过大时直接排除，
// 即使预算足够（100MB 输入要 1.3GB 峰值，换成流式只有 ~2 倍）。
func TestChooseSkipsRungOverItsMaxInput(t *testing.T) {
	rungs := []Candidate{
		{Name: "full+memory", Factor: 13, MaxInputSize: 32 * mib},
		{Name: "stream+memory", Factor: 2},
	}
	loose := Memory{Available: 8 << 30}
	p, _ := Choose(ChooseRequest{Size: 31 * mib, Memory: loose, Candidates: rungs})
	if p.Chosen.Name != "full+memory" {
		t.Fatalf("31MiB < 上限 32MiB，应选整块解析：%+v", p)
	}
	p, _ = Choose(ChooseRequest{Size: 33 * mib, Memory: loose, Candidates: rungs})
	if p.Chosen.Name != "stream+memory" {
		t.Fatalf("33MiB ≥ 上限，应降档：%+v", p)
	}
	if len(p.Rejected) != 1 || !strings.Contains(p.Rejected[0].Why, "本档上限") {
		t.Fatalf("要说明体积上限：%+v", p.Rejected)
	}
}
