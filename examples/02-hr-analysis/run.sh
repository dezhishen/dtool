#!/usr/bin/env bash
# 示例 2：人员分析 —— 前导零工号、可空列、饼图、按日期过滤、表格导出
source "$(dirname "$0")/../lib.sh"
demo_init 02-hr-analysis

dt convert --input "$DATA/hr.xlsx" --name hr
# 观察 Schema：工号保持 string（不会丢前导零）；"绩效"可空；"部门"有 enum
dt datasets show hr --preview-rows 3

# 各部门人数与平均月薪 → 饼图
dt query --sql 'SELECT "部门", COUNT(*) AS 人数, ROUND(AVG("月薪")) AS 平均月薪 FROM hr GROUP BY "部门" ORDER BY 人数 DESC'
viz --input latest:query --type pie --x 部门 --y 人数 --title "部门人数占比" --output headcount.png

# 日期是 ISO 文本，可直接比较；单引号是字符串字面量
dt query --format csv --output recent_hires.csv --sql '
  SELECT "工号", "姓名", "部门", "入职日期" FROM hr WHERE "入职日期" >= '"'2025-01-01'"' ORDER BY "入职日期" DESC'
head -5 recent_hires.csv

# 尚无绩效的员工（NULL 处理），表格导出为 Excel
dt query --sql 'SELECT "工号", "姓名", "入职日期" FROM hr WHERE "绩效" IS NULL ORDER BY "入职日期"'
viz --input latest:query --type table --format xlsx --output no_rating.xlsx

echo; echo "产物：headcount.png  recent_hires.csv  no_rating.xlsx"
