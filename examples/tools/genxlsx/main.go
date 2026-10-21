// genxlsx 生成示例数据源（固定随机种子，结果可复现）：go run ./examples/tools/genxlsx
package main

import (
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"github.com/xuri/excelize/v2"
)

func main() {
	dir := "examples/data"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatal(err)
	}
	r := rand.New(rand.NewSource(42))
	// 按固定顺序生成，保证共享的随机数流可复现
	for _, g := range []struct {
		name string
		gen  func(*rand.Rand) *excelize.File
	}{{"sales.xlsx", sales}, {"hr.xlsx", hr}, {"messy.xlsx", messy}, {"studio.xlsx", studio}} {
		name, gen := g.name, g.gen
		f := gen(r)
		// 固定时间戳保证可复现，并避免写入库默认的作者信息
		_ = f.SetDocProps(&excelize.DocProperties{Creator: "dtool examples", LastModifiedBy: "dtool examples",
			Created: "2026-01-01T00:00:00Z", Modified: "2026-01-01T00:00:00Z"})
		_ = f.SetAppProps(&excelize.AppProperties{Application: "dtool examples"})
		p := filepath.Join(dir, name)
		if err := f.SaveAs(p); err != nil {
			log.Fatal(err)
		}
		f.Close()
		fmt.Println("wrote", p)
	}
}

func put(f *excelize.File, sheet string, rows [][]any, widths ...float64) {
	for i, row := range rows {
		cell, _ := excelize.CoordinatesToCellName(1, i+1)
		if err := f.SetSheetRow(sheet, cell, &row); err != nil {
			log.Fatal(err)
		}
	}
	for i, w := range widths {
		col, _ := excelize.ColumnNumberToName(i + 1)
		_ = f.SetColWidth(sheet, col, col, w)
	}
}

func pick[T any](r *rand.Rand, xs []T) T { return xs[r.Intn(len(xs))] }

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func sales(r *rand.Rand) *excelize.File {
	f := excelize.NewFile()
	f.SetSheetName("Sheet1", "订单")
	f.NewSheet("客户")

	names := []string{"星辰科技", "云帆贸易", "启明电子", "远航物流", "北辰软件", "南山制造", "海川传媒", "恒信金融",
		"嘉禾农业", "朗月设计", "盛世教育", "天成建设", "万象零售", "迅达快递", "卓越咨询"}
	levels := []string{"金牌", "银牌", "普通", "普通", "银牌"}
	cities := []string{"北京", "上海", "广州", "深圳", "成都", "杭州", "重庆", "天津"}
	cust := [][]any{{"客户ID", "客户名称", "等级", "城市", "注册日期"}}
	for i, n := range names {
		d := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, r.Intn(1000))
		cust = append(cust, []any{fmt.Sprintf("C%03d", i+1), n, pick(r, levels), pick(r, cities), d.Format("2006-01-02")})
	}
	put(f, "客户", cust, 10, 14, 8, 8, 14)

	type product struct {
		name  string
		price float64
	}
	products := []product{{"笔记本", 5999}, {"显示器", 1299}, {"键盘", 299}, {"鼠标", 129}, {"耳机", 499}}
	regions := []string{"华北", "华东", "华东", "华南", "西南"}
	statuses := []string{"已完成", "已完成", "已完成", "已完成", "配送中", "已退款"}
	orders := [][]any{{"订单号", "日期", "区域", "客户ID", "产品", "数量", "单价", "金额", "状态"}}
	for i := 1; i <= 120; i++ {
		day := (i-1)*181/120 + r.Intn(2) // 均匀铺满 1–6 月，避免最后一个月只有零星订单
		if day > 180 {
			day = 180
		}
		p := pick(r, products)
		qty := 1 + r.Intn(8)
		price := round2(p.price * pick(r, []float64{1, 1, 0.95, 0.9}))
		d := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, day)
		orders = append(orders, []any{fmt.Sprintf("OD2026-%04d", i), d.Format("2006-01-02"), pick(r, regions),
			fmt.Sprintf("C%03d", 1+r.Intn(len(names))), p.name, qty, price, round2(price * float64(qty)), pick(r, statuses)})
	}
	put(f, "订单", orders, 14, 12, 8, 10, 10, 8, 10, 12, 10)
	return f
}

func hr(r *rand.Rand) *excelize.File {
	f := excelize.NewFile()
	f.SetSheetName("Sheet1", "员工")
	surnames := []string{"王", "李", "张", "刘", "陈", "杨", "赵", "黄", "周", "吴"}
	givens := []string{"伟", "芳", "娜", "敏", "静", "磊", "洋", "艳", "勇", "杰", "涛", "明", "超", "霞", "平"}
	depts := []string{"技术部", "技术部", "技术部", "产品部", "市场部", "销售部", "销售部", "人事部", "财务部"}
	levels := []string{"P3", "P4", "P4", "P5", "P5", "P6", "P7"}
	base := map[string]int{"P3": 9000, "P4": 14000, "P5": 21000, "P6": 30000, "P7": 42000}
	rows := [][]any{{"工号", "姓名", "部门", "职级", "月薪", "入职日期", "绩效", "邮箱"}}
	for i := 0; i < 60; i++ {
		id := fmt.Sprintf("%05d", 101+i) // 文本单元格，保留前导零
		lv := pick(r, levels)
		salary := base[lv] + r.Intn(40)*100
		hire := time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, r.Intn(7*365+270))
		var perf any = pick(r, []string{"A", "B", "B", "C"})
		if hire.Year() >= 2026 {
			perf = nil // 新入职尚无绩效
		}
		rows = append(rows, []any{id, pick(r, surnames) + pick(r, givens), pick(r, depts), lv, salary,
			hire.Format("2006-01-02"), perf, fmt.Sprintf("emp%s@example.com", id)})
	}
	put(f, "员工", rows, 10, 8, 10, 8, 10, 12, 8, 26)
	return f
}

// studio 是「5 人设计工作室经营分析」场景的数据源，四张表放在同一个工作簿里：
// 员工（10 人：5 名核心设计 + 5 名支持/兼职）、项目（40 个）、报销（60 笔）、
// 考勤（9 个工作日 × 10 人 = 90 条）。
//
// 数据里刻意留了几处「真实世界会长这样」的东西：两单低价高工时的亏损项目、
// 三成报销没有项目归属（管理费）、少量驳回的报销、请假与加班并存。
func studio(r *rand.Rand) *excelize.File {
	f := excelize.NewFile()
	f.SetSheetName("Sheet1", "员工")
	f.NewSheet("项目")
	f.NewSheet("报销")
	f.NewSheet("考勤")

	emp := [][]any{
		{"员工ID", "姓名", "岗位", "级别", "入职日期", "月薪"},
		{"E01", "沈一", "设计总监", "P8", "2019-03-04", 45000},
		{"E02", "陆遥", "资深设计师", "P6", "2021-07-12", 28000},
		{"E03", "方竹", "设计师", "P5", "2022-09-01", 22000},
		{"E04", "何蓝", "设计师", "P5", "2023-04-17", 21500},
		{"E05", "苏晴", "视觉设计", "P4", "2024-02-19", 18000},
		{"E06", "郑维", "项目管理", "P6", "2020-11-02", 25000},
		{"E07", "林默", "客户执行", "P4", "2023-08-07", 16000},
		{"E08", "吴桐", "行政人事", "P3", "2022-01-10", 12000},
		{"E09", "秦野", "兼职设计师", "P4", "2025-03-03", 9000},
		{"E10", "顾南", "兼职摄影", "P4", "2025-06-16", 8000},
	}
	put(f, "员工", emp, 10, 8, 12, 8, 12, 10)

	clients := []string{"晨光文创", "山海茶饮", "北方书店", "霁云咖啡", "拾光民宿", "沐野户外",
		"知行教育", "蓝湾剧场", "和风家居", "星野宠物", "素问医疗", "云台科技"}
	kinds := []struct {
		name   string
		lo, hi int // 合同金额区间（元）
		hLo, h int // 投入工时区间（小时）
	}{
		{"品牌设计", 80000, 180000, 200, 380},
		{"UI设计", 60000, 150000, 180, 320},
		{"包装设计", 30000, 90000, 100, 220},
		{"空间展陈", 120000, 300000, 300, 520},
		{"插画", 15000, 50000, 60, 130},
	}
	owners := []string{"E01", "E02", "E02", "E03", "E03", "E04", "E05", "E06"}
	proj := [][]any{{"项目ID", "项目名称", "客户", "项目类型", "开始日期", "结束日期",
		"合同金额", "投入工时", "负责人ID", "状态"}}
	for i := 1; i <= 40; i++ {
		k := kinds[r.Intn(len(kinds))]
		client := pick(r, clients)
		start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, r.Intn(240))
		end := start.AddDate(0, 0, 20+r.Intn(100))
		amount := float64(k.lo + r.Intn((k.hi-k.lo)/1000+1)*1000)
		hours := float64(k.hLo + r.Intn((k.h-k.hLo)/10+1)*10)
		status := pick(r, []string{"已完成", "已完成", "已完成", "进行中", "已暂停"})
		// 两单刻意做成亏损：低价 + 高工时（改稿与需求变更的典型结果）
		if i == 7 || i == 23 {
			amount = float64(k.lo)
			hours = float64(k.h) * 1.6
		}
		var endCell any = end.Format("2006-01-02")
		if status == "进行中" {
			endCell = nil
		}
		proj = append(proj, []any{fmt.Sprintf("PRJ2026-%03d", i),
			client + k.name, client, k.name,
			start.Format("2006-01-02"), endCell, amount, hours, pick(r, owners), status})
	}
	put(f, "项目", proj, 14, 22, 12, 10, 12, 12, 12, 10, 10, 8)

	cats := []struct {
		name   string
		lo, hi float64
	}{
		{"差旅", 300, 3200},
		{"软件订阅", 99, 1200},
		{"硬件", 500, 9000},
		{"餐饮", 80, 900},
		{"外包", 2000, 18000},
		{"办公", 50, 600},
	}
	emps := []string{"E01", "E02", "E03", "E04", "E05", "E06", "E07", "E08", "E09", "E10"}
	bx := [][]any{{"报销ID", "日期", "员工ID", "项目ID", "费用类型", "金额", "状态"}}
	for i := 1; i <= 60; i++ {
		c := cats[r.Intn(len(cats))]
		var pid any // 三成报销是行政/公共开支，没有项目归属
		if r.Intn(10) < 7 {
			pid = fmt.Sprintf("PRJ2026-%03d", 1+r.Intn(40))
		}
		d := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, r.Intn(270))
		bx = append(bx, []any{fmt.Sprintf("BX2026-%03d", i), d.Format("2006-01-02"),
			pick(r, emps), pid, c.name, round2(c.lo + r.Float64()*(c.hi-c.lo)),
			pick(r, []string{"已批", "已批", "已批", "已批", "已批", "已批", "已批", "待批", "待批", "驳回"})})
	}
	put(f, "报销", bx, 14, 12, 10, 14, 12, 12, 8)

	att := [][]any{{"日期", "员工ID", "出勤类型", "工时", "加班工时"}}
	day := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	for n := 0; n < 9; { // 9 个工作日 × 10 人 = 90 条
		if wd := day.Weekday(); wd != time.Saturday && wd != time.Sunday {
			for _, e := range emps {
				kind := pick(r, []string{"出勤", "出勤", "出勤", "出勤", "出勤", "出勤", "出勤", "远程", "远程", "请假"})
				hours, ot := 8.0, 0.0
				switch {
				case kind == "请假":
					hours = 0
				case r.Intn(3) == 0:
					ot = float64(1 + r.Intn(4))
				}
				att = append(att, []any{day.Format("2006-01-02"), e, kind, hours, ot})
			}
			n++
		}
		day = day.AddDate(0, 0, 1)
	}
	put(f, "考勤", att, 12, 10, 12, 8, 10)
	return f
}

// messy 故意包含常见脏数据：空表头、重复表头、空行、合并单元格、前导零、混合类型、短行。
func messy(*rand.Rand) *excelize.File {
	f := excelize.NewFile()
	f.SetSheetName("Sheet1", "原始数据")
	put(f, "原始数据", [][]any{
		{"编号", "", "名称", "名称", "手机号", "金额", "数量"},
		{1001, "华北仓", "机箱", "A款", "013800138000", 100, 2},
		{1002, "", "电源", "B款", "013900139000", 200.5, 5},
		{},
		{1003, "华南仓", "风扇", "C款", "", "N/A", 7},
		{1004, "华南仓", "显卡", "", "013700137000", 5999},
	}, 8, 10, 10, 10, 16, 10, 8)
	if err := f.MergeCell("原始数据", "B2", "B3"); err != nil {
		log.Fatal(err)
	}
	return f
}
