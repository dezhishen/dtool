#!/usr/bin/env bash
# 示例 5：多系列图表 —— 长表 --group-by 透视、宽表多 --y 列
source "$(dirname "$0")/../lib.sh"
demo_init 05-multi-series

# 0. 先把订单工作表变成数据集，后续两条查询都复用它
dt convert --input "$DATA/sales.xlsx" --sheet 订单 --name sales

# 1. 长表 → 簇状柱：一条 (区域, 月份) 汇总结果，按月份拆成系列
dt query --sql '
  SELECT "区域", substr("日期", 1, 7) AS "月份", SUM("金额") AS 销售额
  FROM sales WHERE "状态" <> '"'已退款'"'
  GROUP BY "区域", "月份" ORDER BY "月份", "区域"'
viz --input latest:query --type bar --x 区域 --y 销售额 --group-by 月份 \
  --title "各区域月度销售额" --output grouped_bar.png

# 2. 同一份长表用 line + --missing gap：缺失的 (区域, 月份) 直接断线
viz --input latest:query --type line --x 月份 --y 销售额 --group-by 区域 --missing gap \
  --title "各区域月度趋势（缺口断线）" --output region_gap.png

# 3. 宽表 → 多折线：一个 SQL 直接产出多个指标列，--y 用逗号分隔
dt query --sql '
  SELECT substr("日期", 1, 7) AS "月份",
         SUM(CASE WHEN "状态" = '"'已完成'"' THEN "金额" ELSE 0 END) AS "已完成",
         SUM(CASE WHEN "状态" = '"'配送中'"' THEN "金额" ELSE 0 END) AS "配送中"
  FROM sales GROUP BY 1 ORDER BY 1'
viz --input latest:query --type line --x 月份 --y 已完成,配送中 \
  --title "各状态月度销售额" --output multi_line.png

echo; echo "产物：grouped_bar.png  region_gap.png  multi_line.png  以及 .dtool/（数据集与 Action 记录）"
