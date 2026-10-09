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
	}{{"sales.xlsx", sales}, {"hr.xlsx", hr}, {"messy.xlsx", messy}} {
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
