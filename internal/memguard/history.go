package memguard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// 「按执行过的记录做数学计算」的落盘部分：每跑完一次装载，就把「预计 vs 实测」写成
// 一条样本；选档时用这些样本估计「这一档能不能过」（TryChance）。
//
// 为什么要落盘而不是只放在内存里：fatal OOM 时进程直接死，内存里什么都没了；而恰恰
// 是那种情况最需要「下次别再试这一档」。工作区是天然的记忆载体，AI 也能直接读。

// planVersion 是文件格式版本，将来加字段时好判断。
const planVersion = 1

// maxSamples 是保留的样本条数（按时间裁剪）：文件要小，且旧数据（换机器、换输入
// 形状）的参考价值本来就低。
const maxSamples = 200

// Sample 是一次装载的实测记录。
type Sample struct {
	Rung      string    `json:"rung"`             // 执行档名，如 "stream+disk"
	Size      uint64    `json:"size"`             // 输入体积
	Predicted uint64    `json:"predicted"`        // 装载前预计峰值
	Peak      uint64    `json:"peak"`             // 实测峰值（RSS/提交量）
	OK        bool      `json:"ok"`               // 是否跑完（false 含被中止/被杀）
	MS        int64     `json:"ms"`               // 耗时
	Source    string    `json:"source,omitempty"` // 预算来源（排查用）
	At        time.Time `json:"at"`
}

// Stats 是从样本里算出来的派生结论（存起来便于人和 AI 直接读，不必自己算分位）。
type Stats struct {
	Rung     string  `json:"rung"`
	N        int     `json:"n"`
	OKRate   float64 `json:"ok_rate"`
	RatioP10 float64 `json:"ratio_p10"` // 实测/预计 的 10 分位
	RatioP50 float64 `json:"ratio_p50"`
	RatioP90 float64 `json:"ratio_p90"`
	MSMedian int64   `json:"ms_median"`
}

// History 是工作区里的样本集（默认 .dtool/plans/samples.json）。
type History struct {
	Version int      `json:"version"`
	Samples []Sample `json:"samples"`
	Stats   []Stats  `json:"stats,omitempty"` // 每次保存时重算
	path    string   `json:"-"`
}

// LoadHistory 读取工作区里的记录；文件不存在（第一次跑）返回空集而不是错误。
func LoadHistory(path string) (*History, error) {
	h := &History{Version: planVersion, path: path}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return h, nil
		}
		return h, err
	}
	if err := json.Unmarshal(b, h); err != nil {
		// 记录坏了不该影响正事：当作没有历史。
		return &History{Version: planVersion, path: path}, nil
	}
	h.path = path
	return h, nil
}

// Add 追加一条样本并按时间裁剪；同时重算派生统计。
func (h *History) Add(s Sample) {
	if s.At.IsZero() {
		s.At = time.Now()
	}
	h.Samples = append(h.Samples, s)
	if len(h.Samples) > maxSamples {
		h.Samples = h.Samples[len(h.Samples)-maxSamples:]
	}
	h.refresh()
}

// refresh 重算派生统计（"数学公式的计算"部分的结果也一并落盘）。
func (h *History) refresh() {
	byRung := map[string][]Sample{}
	var order []string
	for _, s := range h.Samples {
		if _, ok := byRung[s.Rung]; !ok {
			order = append(order, s.Rung)
		}
		byRung[s.Rung] = append(byRung[s.Rung], s)
	}
	sort.Strings(order)
	h.Stats = nil
	for _, rung := range order {
		ss := byRung[rung]
		var ratios, mss []float64
		ok := 0
		for _, s := range ss {
			if s.OK {
				ok++
			}
			if r := ratio(s); r > 0 {
				ratios = append(ratios, r)
			}
			if s.MS > 0 {
				mss = append(mss, float64(s.MS))
			}
		}
		h.Stats = append(h.Stats, Stats{Rung: rung, N: len(ss),
			OKRate:   float64(ok) / float64(len(ss)),
			RatioP10: Quantile(ratios, 0.10), RatioP50: Quantile(ratios, 0.50), RatioP90: Quantile(ratios, 0.90),
			MSMedian: int64(Quantile(mss, 0.50))})
	}
}

// Save 原子写回（先写临时文件再改名），避免崩溃时留下半个文件——这份记录存在的意义
// 正是给崩溃后的下一次运行看。
func (h *History) Save() error {
	if h.path == "" {
		return nil
	}
	if h.Version == 0 {
		h.Version = planVersion
	}
	h.refresh()
	if err := os.MkdirAll(filepath.Dir(h.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	tmp := h.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, h.path)
}

// StatOf 返回某个档的派生统计；没有则返回零值。
func (h *History) StatOf(rung string) (Stats, bool) {
	for _, s := range h.Stats {
		if s.Rung == rung {
			return s, true
		}
	}
	return Stats{}, false
}

// PlanFile 返回工作区里的记录文件路径。
func PlanFile(workspace string) string { return filepath.Join(workspace, "plans", "samples.json") }
