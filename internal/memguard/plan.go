package memguard

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// 策略阶梯：把「内存上限」当作**选档的输入**，而不是「过/不过的判决」。
//
// 为什么必须分档（数字见 docs/PERFORMANCE.md，1 核 + cgroup 实测，123MB 输入）：
//
//	full   + 内存库 ≈ 7.8–13× 输入体积（Go 堆是大头，超限 = fatal，不可恢复）
//	stream + 内存库 ≈ 1.4–2.4×（内存库必须自己装下全部页面，跑得快但吃提交量）
//	stream + 磁盘库 ≈ 0.16×（页缓存变成文件页，可被系统回收；耗时几乎不变）
//
// 磁盘库那一档是关键：它把 123MB 输入在 256MB 硬上限下的峰值从 176MB 压到 20MB，
// 于是「预检拒绝」可以变成「换个档照跑」。所以选档顺序是「快到省」，而不是先拦截。

// Candidate 是一个可选执行档。峰值按 Base + size×Factor 估算：
// Factor 是**增量**倍率（实测得来），Base 是该档的固定开销（Go runtime + SQLite
// 页缓存基线）。小输入时 Base 是主导项——没有它，几百字节的输入会被估成几十字节，
// 「预算 8B」这种极端配置下反而会误判成放得下。
type Candidate struct {
	Name   string  `json:"name"`
	Factor float64 `json:"factor"`
	Base   uint64  `json:"base"`
	Note   string  `json:"note,omitempty"`
}

// Predicted 按输入体积估算该档峰值。
func (c Candidate) Predicted(size uint64) uint64 {
	if c.Factor <= 0 && c.Base == 0 {
		return 0
	}
	return c.Base + uint64(float64(size)*c.Factor)
}

// 硬上限的额外折扣：看门狗 200ms 采一次样，而一次大分配可以在两次采样之间把提交量
// 顶上去。超软预算只是普通错误，超硬上限是 OOM-kill / runtime fatal——不可恢复，
// 所以硬上限再多留 20%。
const hardExtraPercent = 80

// Threshold 返回该预算下「允许使用」的量：软预算直接给 Available，硬上限再打八折。
func (m Memory) Threshold() uint64 {
	if m.Available == 0 {
		return 0
	}
	if !m.Hard {
		return m.Available
	}
	return m.Available * hardExtraPercent / 100
}

// 擦边判定的默认参数。
const (
	// DefaultMinChance：估算放不下但历史显示有这么大概率能过时，仍然值得试一下
	// （试错的收益是速度，代价是重跑一次）。
	DefaultMinChance = 0.5
	// priorRatioMean/priorRatioSD：没有任何历史时的先验分布，描述「实测峰值/预计峰值」。
	// 实测样本落在 0.5–1.2（预计倍率本身是保守上界，所以中位数小于 1）。
	priorRatioMean = 0.85
	priorRatioSD   = 0.30
	// priorWeight：先验等价于几个样本（样本少时靠它把概率拉回保守侧）。
	priorWeight = 3.0
)

// Policy 决定「所有档都预计放不下」时怎么办。
type Policy string

const (
	// PolicyTry：仍然试最省的一档（失败会被记录成 Action，AI 可据此换档重试）。
	PolicyTry Policy = "try"
	// PolicyStrict：直接失败，不试（结果是可读的错误 JSON，不是崩溃）。
	PolicyStrict Policy = "strict"
)

// ParsePolicy 解析 --mem-policy；空串按 try。
func ParsePolicy(s string) (Policy, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(PolicyTry):
		return PolicyTry, nil
	case string(PolicyStrict):
		return PolicyStrict, nil
	}
	return "", fmt.Errorf("invalid memory policy %q", s)
}

// Rejected 记录一个被跳过的档及原因（meminfo / 日志用，便于解释「为什么慢」）。
type Rejected struct {
	Name      string  `json:"name"`
	Predicted uint64  `json:"predicted"`
	Threshold uint64  `json:"threshold"`
	Chance    float64 `json:"chance"`
	Why       string  `json:"why"`
}

// Plan 是选档结果。
type Plan struct {
	Chosen    Candidate  `json:"chosen"`
	Predicted uint64     `json:"predicted"`
	Threshold uint64     `json:"threshold"`
	Headroom  float64    `json:"headroom"` // Threshold/Predicted
	Chance    float64    `json:"chance"`   // 按历史估计「试这一档能过」的概率
	Forced    bool       `json:"forced"`   // 操作者显式指定
	Risky     bool       `json:"risky"`    // 所有档都预计放不下，仍按策略试
	Reason    string     `json:"reason"`
	Rejected  []Rejected `json:"rejected,omitempty"`
}

// ChooseRequest 是选档的全部输入。
type ChooseRequest struct {
	Size       uint64      // 输入体积（多源时给合计）
	Memory     Memory      // 预算（含来源与是否硬上限）
	Candidates []Candidate // 从快到省排列
	Forced     string      // 操作者强制指定的档名；空 = 自动
	Policy     Policy      // 空 = PolicyTry
	MinChance  float64     // 值得一试的最低成功率；0 = DefaultMinChance
	Samples    []Sample    // 本工作区已执行过的记录（可为空）
}

// Choose 按「快到省」挑一档：预计放得下就直接用；预计放不下但历史显示有机会就试；
// 全部都没机会时按 Policy 决定「试最省档」还是「交给调用方失败」。
//
// 这里不做任何 IO、不持有状态，纯函数——所有判断都能在 Linux 单测里跑，包括 Windows
// 才有的 Job Object 预算（用 Memory{Hard:true} 构造即可）。
func Choose(r ChooseRequest) (Plan, error) {
	if len(r.Candidates) == 0 {
		return Plan{}, fmt.Errorf("no candidates")
	}
	if r.Forced != "" {
		for _, c := range r.Candidates {
			if c.Name == r.Forced {
				p := r.plan(c)
				p.Forced = true
				p.Reason = fmt.Sprintf("操作者指定 %s（预计峰值 %s，阈值 %s）", c.Name, HumanSize(p.Predicted), HumanSize(p.Threshold))
				return p, nil
			}
		}
		names := make([]string, 0, len(r.Candidates))
		for _, c := range r.Candidates {
			names = append(names, c.Name)
		}
		return Plan{}, fmt.Errorf("未知的执行档 %q", r.Forced)
	}

	policy := r.Policy
	if policy == "" {
		policy = PolicyTry
	}
	minChance := r.MinChance
	if minChance <= 0 {
		minChance = DefaultMinChance
	}

	var rejected []Rejected
	// 预算未知（不检查）：没有依据可比，直接用最快的档。
	if r.Memory.Threshold() == 0 || r.Size == 0 {
		p := r.plan(r.Candidates[0])
		p.Reason = "无内存预算可比对，直接用最快档"
		p.Rejected = rejected
		return p, nil
	}

	leanest := r.Candidates[len(r.Candidates)-1]
	for _, c := range r.Candidates {
		p := r.plan(c)
		if p.Predicted <= p.Threshold {
			if len(rejected) > 0 {
				p.Reason = fmt.Sprintf("预计峰值 %s ≤ 阈值 %s，放得下", HumanSize(p.Predicted), HumanSize(p.Threshold))
			} else {
				p.Reason = fmt.Sprintf("最快档且预计峰值 %s ≤ 阈值 %s", HumanSize(p.Predicted), HumanSize(p.Threshold))
			}
			p.Rejected = rejected
			return p, nil
		}
		if p.Chance >= minChance {
			p.Reason = fmt.Sprintf("预计放不下（%s > 阈值 %s），但历史成功率 %.0f%% ≥ %.0f%%，值得一试",
				HumanSize(p.Predicted), HumanSize(p.Threshold), p.Chance*100, minChance*100)
			p.Rejected = rejected
			return p, nil
		}
		rejected = append(rejected, Rejected{Name: c.Name, Predicted: p.Predicted,
			Threshold: p.Threshold, Chance: p.Chance,
			Why: fmt.Sprintf("预计 %s > 阈值 %s，成功率仅 %.0f%%",
				HumanSize(p.Predicted), HumanSize(p.Threshold), p.Chance*100)})
	}

	p := r.plan(leanest)
	p.Rejected = rejected
	p.Risky = true
	if policy == PolicyStrict {
		p.Reason = fmt.Sprintf("最省档 %s 预计仍需 %s > 阈值 %s，按 --mem-policy strict 不尝试",
			leanest.Name, HumanSize(p.Predicted), HumanSize(p.Threshold))
		return p, nil
	}
	p.Reason = fmt.Sprintf("所有档预计都超出阈值，仍试最省档 %s（预计 %s，历史成功率 %.0f%%）；失败会记录在 Action 里，可换更省档或拆分输入",
		leanest.Name, HumanSize(p.Predicted), p.Chance*100)
	return p, nil
}

// plan 填充单个档的判定数字。
func (r ChooseRequest) plan(c Candidate) Plan {
	pred := c.Predicted(r.Size)
	p := Plan{Chosen: c, Predicted: pred, Threshold: r.Memory.Threshold()}
	if p.Threshold > 0 {
		p.Headroom = float64(pred) / float64(p.Threshold)
	}
	p.Chance = TryChance(r.Samples, c.Name, pred, p.Threshold)
	return p
}

// TryChance 估计「按预计峰值 pred、阈值 threshold，这一档实际能跑过去」的概率：
//
//	p = P(ratio ≤ threshold/pred)，ratio = 实测峰值 / 预计峰值
//
// ratio 的分布来自本工作区执行过的记录（同档样本优先，样本不足时混入全部档），
// 再与实测先验（正态 priorRatioMean/SD）按 priorWeight 混合——样本少时结论必须偏保守。
func TryChance(samples []Sample, rung string, pred, threshold uint64) float64 {
	if pred == 0 || threshold == 0 {
		return 1 // 无从判断时不拦
	}
	h := float64(threshold) / float64(pred)
	ratios := ratiosOf(samples, rung)
	n := float64(len(ratios))
	emp := empiricalCDF(ratios, h)
	pPrior := normalCDF((h - priorRatioMean) / priorRatioSD)
	return (n*emp + priorWeight*pPrior) / (n + priorWeight)
}

// ratiosOf 收集 ratio 样本：优先同档，样本少于 5 个时并入其他档（倍率不同但
// 「预计 vs 实测」的偏差形态是共通的），并把异常值挡掉。
func ratiosOf(samples []Sample, rung string) []float64 {
	var same, all []float64
	for _, s := range samples {
		r := ratio(s)
		if r <= 0 {
			continue
		}
		all = append(all, r)
		if s.Rung == rung {
			same = append(same, r)
		}
	}
	if len(same) >= 5 {
		return same
	}
	return append(same, all...)
}

// ratio 返回实测/预计；预计缺失时用 0（丢弃该样本）。
func ratio(s Sample) float64 {
	if s.Predicted == 0 || s.Peak == 0 {
		return 0
	}
	return float64(s.Peak) / float64(s.Predicted)
}

// empiricalCDF 返回经验分布 P(x ≤ v)。
func empiricalCDF(xs []float64, v float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	n := 0
	for _, x := range xs {
		if x <= v {
			n++
		}
	}
	return float64(n) / float64(len(xs))
}

// normalCDF 标准正态分布函数（用 erf 近似，够这个用途）。
func normalCDF(z float64) float64 { return 0.5 * (1 + math.Erf(z/math.Sqrt2)) }

// Quantile 返回分位数（用于统计与展示）；xs 会被排序副本处理。
func Quantile(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	ys := append([]float64(nil), xs...)
	sort.Float64s(ys)
	if q <= 0 {
		return ys[0]
	}
	if q >= 1 {
		return ys[len(ys)-1]
	}
	pos := q * float64(len(ys)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return ys[lo]
	}
	return ys[lo] + (ys[hi]-ys[lo])*(pos-float64(lo))
}
