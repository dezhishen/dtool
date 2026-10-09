#!/usr/bin/env bash
# 示例 1：销售报表 —— 多工作表导入、JOIN 查询、柱状图、Excel/Markdown 导出
source "$(dirname "$0")/../lib.sh"
demo_init 01-sales-report

# 1. 同一个工作簿的两个工作表导入为两个独立数据集（Schema 自动生成）
dt convert --input "$DATA/sales.xlsx" --sheet 订单 --name orders
dt convert --input "$DATA/sales.xlsx" --sheet 客户 --name customers
dt datasets list
dt datasets show orders --preview-rows 3          # 先看 Schema：类型/可空/枚举/样例

# 2. 数据集名直接当表名；含中文的列名用双引号
dt query --format markdown --output region.md --sql '
  SELECT "区域", COUNT(*) AS 订单数, SUM("金额") AS 总金额
  FROM orders WHERE "状态" <> '"'已退款'"'
  GROUP BY "区域" ORDER BY 总金额 DESC'
cat region.md

# 3. 用刚才的结果出图（latest:query 引用最近一次成功的查询，无需重跑）
viz --input latest:query --type bar --x 区域 --y 总金额 --title "各区域销售额" --output region.png

# 4. 两个数据集 JOIN：金额 Top 5 客户，并导出 Excel
dt query --format xlsx --output top_customers.xlsx --sql '
  SELECT c."客户名称", c."等级", SUM(o."金额") AS 总金额
  FROM orders o JOIN customers c ON o."客户ID" = c."客户ID"
  GROUP BY c."客户名称", c."等级" ORDER BY 总金额 DESC LIMIT 5'

echo; echo "产物：region.md  region.png  top_customers.xlsx  以及 .dtool/（数据集与 Action 记录）"
