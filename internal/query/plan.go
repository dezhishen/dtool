package query

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dezhishen/dtool/internal/memguard"
	"github.com/dezhishen/dtool/pkg/types"
)

// 执行档（strategy）：把「装入方式」和「库落在哪」两个维度合成一档，由内存预算来选。
//
// 倍率是**保守上界**，取实测值再留余量（1 核 + cgroup，123MB 输入，见 docs/PERFORMANCE.md）：
//
//	实测            倍率        本文件采用
//	full+内存库     7.75×       13    （Go 堆是大头，超了是 runtime fatal）
//	stream+内存库   1.40–2.44×  2     （内存库必须自己装下全部页面）
//	stream+磁盘库   0.12–0.16×  0.3   （页缓存变成文件页，可被系统回收）
//
// 磁盘库这一档是「不拦截」的关键：同一份 123MB 输入，内存库峰值 176MB，磁盘库只有 20MB，
// 耗时几乎一样（12.5s vs 12.6s）——所以在 256MB 硬上限下，换个档就能跑，不必拒绝。

// Store 决定 SQLite 数据库落在哪里。
type Store string

const (
	StoreAuto   Store = "auto"
	StoreMemory Store = "memory"
	StoreDisk   Store = "disk"
)

// ParseStore 解析 --store；空串按 auto。
func ParseStore(s string) (Store, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(StoreAuto):
		return StoreAuto, nil
	case string(StoreMemory):
		return StoreMemory, nil
	case string(StoreDisk):
		return StoreDisk, nil
	}
	return "", types.Errorf(types.CodeUsage, "invalid store %q", s).
		WithHint("可选：auto（按内存预算自适应）/ memory（内存库，快）/ disk（磁盘库，峰值低）")
}

// strategy 是一档具体做法：候选档 + 落库位置 + 装入方式。
type strategy struct {
	memguard.Candidate
	Store Store
	Mode  LoadMode
}

// ladder 是「快到省」的完整阶梯；顺序即优先级。
var ladder = []strategy{
	{Store: StoreMemory, Mode: LoadFull, Candidate: memguard.Candidate{
		Name: "full+memory", Factor: fullPeakFactor, Base: 32 << 20,
		// 峰值是输入的 13 倍：估算擦着阈值过就是等崩，所以要求再留 20% 余量
		// （旧实现里那条「整块解析 ≤ 预算 80%」的规则就落在这里）
		RequireHeadroomPercent: 20,
		// ≥32MB 就走流式：13 倍放大不值得（流式只有 ~1.4 倍），这条与旧的
		// auto 规则一致，也解释了为什么大文件从来不会选整块解析
		MaxInputSize: autoFullMaxSize,
		Note:         "整块解析进内存库（最快，峰值最高；仅 <32MB 且留 20% 余量时选）"}},
	{Store: StoreMemory, Mode: LoadStream, Candidate: memguard.Candidate{
		Name: "stream+memory", Factor: streamPeakFactor, Base: 32 << 20,
		Note: "流式装入内存库（默认）"}},
	{Store: StoreDisk, Mode: LoadStream, Candidate: memguard.Candidate{
		Name: "stream+disk", Factor: diskPeakFactor, Base: 24 << 20,
		Note: "流式装入磁盘库（页缓存可回收，峰值最低）"}},
}

// candidates 按操作者的显式指定过滤阶梯：
//   - 都不指定 → 完整阶梯（自动选档）
//   - 只指定 --load-mode → 保留该装入方式的档（例如 full 只剩 full+memory）
//   - 只指定 --store → 保留该落库位置的档
//   - 两者都指定 → 只剩一档，等于强制；组合不存在时给出可读的用法错误
//
// 操作者想「强行指定方式」时用这两个参数；想省事就留 auto。
func candidates(modeFlag string, storeFlag string) ([]strategy, string, error) {
	mode := strings.ToLower(strings.TrimSpace(modeFlag))
	store := strings.ToLower(strings.TrimSpace(storeFlag))
	if mode != "" && mode != "auto" {
		if _, err := ParseLoadMode(mode); err != nil {
			return nil, "", err
		}
	} else {
		mode = ""
	}
	if _, err := ParseStore(store); err != nil {
		return nil, "", err
	}
	if store == "auto" {
		store = ""
	}

	out := make([]strategy, 0, len(ladder))
	for _, s := range ladder {
		if mode != "" && string(s.Mode) != mode {
			continue
		}
		if store != "" && string(s.Store) != store {
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, "", types.Errorf(types.CodeUsage, "没有这样的执行档：--load-mode=%s --store=%s", modeFlag, storeFlag).
			WithHint("full 只能配 memory（峰值来自 Go 堆，换磁盘库不省）；stream 可配 memory 或 disk")
	}
	forced := ""
	if len(out) == 1 {
		forced = out[0].Name // 只剩一档：就是操作者要的那档
	}
	return out, forced, nil
}

// strategyByName 按档名取回完整定义。
func strategyByName(name string) strategy {
	for _, s := range ladder {
		if s.Name == name {
			return s
		}
	}
	return ladder[len(ladder)-1]
}

// rungNames 列出档名（报错信息用）。
func rungNames(ss []strategy) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.Name)
	}
	return out
}

// candidateList 抽出 memguard 需要的候选切片。
func candidateList(ss []strategy) []memguard.Candidate {
	out := make([]memguard.Candidate, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.Candidate)
	}
	return out
}

// storeDSN 生成连接串。
//
// 两处 pragma 都是有来历的（1 核 + cgroup，123MB / 70 万行，`GROUP BY <高基数列>`）：
//
//	journal_mode(OFF)+synchronous(OFF)  磁盘库是一次性产物，掉电丢失无所谓，省掉 fsync
//	temp_store(FILE)                   **临时表必须落盘**：GROUP BY/ORDER BY 在没有索引时
//	                                   要先排序，temp_store=MEMORY 会把排序器整个塞进内存
//	                                   ——实测聚合段从 +5MB 涨到 +42MB，而磁盘库的加载段
//	                                   本来只有 33MB，等于白省；改成 FILE 后总峰值
//	                                   75MB → 52MB，耗时不变（1.78s vs 1.79s）。
//	                                   这一条同时解释了 Windows 上「disk 档不降峰值」：
//	                                   不是磁盘库没用，是排序器在内存里顶着。
//	cache_size(-8000)                 页缓存 8MB（磁盘库），配合落盘的临时表把提交量压低
func storeDSN(st Store, dbPath string) string {
	if st == StoreDisk {
		return "file:" + filepath.ToSlash(dbPath) +
			"?_pragma=journal_mode(OFF)&_pragma=synchronous(OFF)&_pragma=temp_store(FILE)&_pragma=cache_size(-8000)"
	}
	return ":memory:?_pragma=temp_store(FILE)"
}

// openStore 打开装载用的数据库。磁盘库返回清理函数（关闭并删除临时文件）：
// 刻意用系统临时目录而不是工作区——它是过程产物，不该留在 .dtool 里。
func openStore(ctx context.Context, st Store) (*sql.DB, func(), error) {
	dsn, dir := storeDSN(st, ""), ""
	if st == StoreDisk {
		d, err := os.MkdirTemp("", "dtool-load-")
		if err != nil {
			return nil, nil, types.Errorf(types.CodeGeneral, "创建临时目录失败：%v", err).
				WithHint("磁盘库需要可写的临时目录（TMPDIR）；也可用 --store memory 换回内存库")
		}
		dir = d
		dsn = storeDSN(st, filepath.Join(dir, "load.db"))
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
		return nil, nil, err
	}
	db.SetMaxOpenConns(1) // 内存库按连接隔离；磁盘库同样保持单连接以免竞争
	cleanup := func() {
		_ = db.Close()
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
	}
	return db, cleanup, nil
}

// planNote 把选档结果翻成一行日志（DTOOL_DEBUG_MEMORY / 降级提示用）。
func planNote(plan memguard.Plan, forced bool) string {
	parts := []string{
		fmt.Sprintf("执行档=%s（预计峰值 %s，阈值 %s，富余 %.2f，历史成功率 %.0f%%）",
			plan.Chosen.Name, memguard.HumanSize(plan.Predicted), memguard.HumanSize(plan.Threshold),
			plan.Headroom, plan.Chance*100),
	}
	if forced {
		parts = append(parts, "操作者强制指定")
	}
	for _, rj := range plan.Rejected {
		parts = append(parts, fmt.Sprintf("跳过 %s：%s", rj.Name, rj.Why))
	}
	return strings.Join(parts, "；")
}

// Ladder 返回完整阶梯（meminfo 展示与文档引用用）。
func Ladder() []memguard.Candidate { return candidateList(ladder) }

// previewPlan 是 PreviewPlan 的实现：不读文件内容，只按体积与预算预演整条阶梯。
func previewPlan(loadMode, store, memPolicy string, mem memguard.Memory, planFile string,
	srcs map[string]string) (LadderPreview, error) {
	cands, forced, err := candidates(loadMode, store)
	if err != nil {
		return LadderPreview{}, err
	}
	aliases := make([]string, 0, len(srcs))
	for a := range srcs {
		aliases = append(aliases, a)
	}
	sort.Strings(aliases)

	var total uint64
	sizeOf := make(map[string]uint64, len(aliases))
	for _, a := range aliases {
		sizeOf[a] = memguard.SizeOf(srcs[a])
		total += sizeOf[a]
	}

	cands, excluded := planCandidates(cands, mem, forced)

	hist, err := memguard.LoadHistory(planFile)
	if err != nil {
		hist = &memguard.History{}
	}
	plan, err := memguard.Choose(memguard.ChooseRequest{
		Size: total, Memory: mem, Candidates: candidateList(cands), Forced: forced,
		Policy: policyOf(memPolicy), Samples: hist.Samples,
	})
	if err != nil {
		return LadderPreview{}, types.Errorf(types.CodeUsage, "%v", err)
	}
	spec := strategyByName(plan.Chosen.Name)

	if excluded != "" && !plan.Forced {
		plan.Reason = joinReason(plan.Reason, excluded)
	}
	out := LadderPreview{
		Chosen: spec.Name, Reason: plan.Reason, Chance: plan.Chance,
		Predicted: plan.Predicted, Threshold: plan.Threshold, Risky: plan.Risky, Forced: plan.Forced,
	}
	switch {
	case plan.Threshold == 0:
		out.Verdict = "ok" // 没有预算可比对，不拦
	case plan.Risky:
		out.Verdict = "risky" // 预计放不下：默认仍会试，失败记录在 Action 里
	case plan.Predicted > plan.Threshold:
		out.Verdict = "borderline" // 预计放不下但历史成功率够高，值得一试
	default:
		out.Verdict = "ok"
	}
	if plan.Forced {
		out.Verdict = "forced"
	}

	for _, a := range aliases {
		size := sizeOf[a]
		out.Sources = append(out.Sources, Preview{
			Alias: a, Path: srcs[a], Size: size, SizeHuman: memguard.HumanSize(size),
			Mode: string(spec.Mode), Store: string(spec.Store),
			Need: spec.Predicted(size), NeedHuman: memguard.HumanSize(spec.Predicted(size)),
			Factor: spec.Factor, Chosen: true,
			Chance: memguard.TryChance(hist.Samples, spec.Name, spec.Predicted(size), plan.Threshold),
		})
	}
	req := memguard.ChooseRequest{Size: total, Memory: mem,
		Candidates: candidateList(cands), Policy: policyOf(memPolicy), Samples: hist.Samples}
	for _, c := range cands {
		pred, calib, n := req.Calibrated(c.Candidate)
		raw := c.Predicted(total)
		out.Rungs = append(out.Rungs, RungPreview{
			Name: c.Name, Note: c.Note, Need: pred, NeedHuman: memguard.HumanSize(pred),
			RawNeed: raw, RawNeedHuman: memguard.HumanSize(raw),
			Calibration: calib, CalibSample: n,
			Chance: memguard.TryChance(hist.Samples, c.Name, pred, plan.Threshold),
			Chosen: c.Name == spec.Name,
		})
	}
	return out, nil
}

// modeNote 返回装入方式的可读说明（进度提示用）。
func modeNote(m LoadMode) string {
	if m == LoadStream {
		return "流式解析"
	}
	return "整块解析"
}

// planCandidates 返回实际参与选档的档位，并给出被排除的原因。
//
// 之所以要抽出来给两条路径共用：`query` 与 `meminfo` 各写一份排除逻辑，就出现
// 「meminfo 预演说会选 full+memory，实际 query 却用 stream+memory」这种自相矛盾——
// 预演存在的意义就是回答「这条命令会怎么跑」，它必须和真实执行用同一套判断。
func planCandidates(cands []strategy, mem memguard.Memory, forced string) ([]strategy, string) {
	if !mem.Uncertain || forced != "" {
		return cands, ""
	}
	// 上限读不到时（Windows Job 标志位设了、值却是 0）：Available 只是「本机空闲内存」，
	// 不是「允许你用的量」。排除峰值比输入大一个数量级的整块解析档——沙箱真限制 256MB
	// 而输入 118MB 时，整块解析会撞上不可恢复的硬上限。操作者显式指定时不干预。
	kept := make([]strategy, 0, len(cands))
	for _, c := range cands {
		if c.Mode == LoadFull {
			memguard.Debugf("上限读不到（%s）：排除 %s", mem.Source, c.Name)
			continue
		}
		kept = append(kept, c)
	}
	if len(kept) == 0 {
		return cands, ""
	}
	return kept, "Job Object 上限读不到（预算按本机空闲内存算，不可信），已排除整块解析档"
}
