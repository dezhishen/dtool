#!/usr/bin/env bash
# 示例 4：pipeline 一条龙 + Action 协作 —— 复用数据集、注释、血缘追踪、失败诊断
source "$(dirname "$0")/../lib.sh"
demo_init 04-pipeline-actions

# 一条命令完成 转换 → 查询 → 折线图（SQL 中用 data 指代刚转换的数据）
dt pipeline --excel "$DATA/sales.xlsx" --sheet 订单 --name orders \
  --sql 'SELECT substr("日期", 1, 7) AS 月份, ROUND(SUM("金额")) AS 月销售额 FROM data GROUP BY 1 ORDER BY 1' \
  --chart line --x 月份 --y 月销售额 --title "月销售额趋势" --output trend.png \
  ${FONT_ARGS[@]+"${FONT_ARGS[@]}"} --tags demo,trend

# 数据已持久化为数据集，之后可以只做查询（不再转换）
dt pipeline --input dataset:orders --sql 'SELECT "产品", SUM("数量") AS 销量 FROM data GROUP BY 1 ORDER BY 2 DESC'

# 给最近一次查询加注释（口径说明会留在 Action 里，之后任何读取者都能看到）
QID="$("$DTOOL" actions list --type query --limit 1 | grep -m1 '"id"' | cut -d'"' -f4)"
dt actions annotate "$QID" --text "口径：不含退款订单需另行核对" --by user

# 基于上次结果派生新查询：--from 只记录血缘
dt query --from "action:$QID" --sql 'SELECT "状态", COUNT(*) AS 订单数 FROM orders GROUP BY 1'
dt actions trace "$QID"

# 故意写错一个字段：失败也会被记录，错误里带可操作的提示
dt query --sql 'SELECT 不存在的列 FROM orders' || true
dt actions list --status failed --limit 1

dt actions list --limit 8
