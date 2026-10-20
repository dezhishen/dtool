#!/usr/bin/env bash
# 示例 3：脏数据 —— 空表头/重复表头/空行/合并单元格/前导零/混合类型，看 warnings 与 Schema 如何处理
source "$(dirname "$0")/../lib.sh"
# demo_init 会 cd 到输出目录，所以在切换前先固定好本示例目录（layout.json 在它下面）
SRC_DIR="$(cd "$(dirname "$0")" && pwd)"
demo_init 03-messy-data

LAYOUT="$SRC_DIR/layout.json"

# 1) 预演练（只读）：表头行可能是第几行、每列还能是什么类型，各带打分与影响（列数/样本行数/列宽是否对齐）
dt convert --input "$DATA/messy.xlsx" --rehearse

# 2) 看结构（只读）：当前方案 + 需要人定的问题（questions）与自动处理的点（warnings）
#    规则能定的已经定了；只有「过滤后仍剩多个候选」才会出现 columns[].candidates
dt convert --input "$DATA/messy.xlsx" --inspect

# 3) 按确认稿转换：layout.json 是人工确认过的结构（金额含 N/A 只能是 string、手机号保留前导零），
#    带上它就不再自动探测；结构与文件对不上会直接报错
dt convert --input "$DATA/messy.xlsx" --layout "$LAYOUT" --name messy
dt datasets show messy

# "金额"含 N/A，整列被判为 string；需要计算时显式转换（NULLIF 把 N/A 变成 NULL，而不是被转成 0）
dt query --sql 'SELECT "编号", "手机号", CAST(NULLIF("金额", '"'N/A'"') AS REAL) AS 金额 FROM messy ORDER BY 1'
# 手机号保留了前导零；缺失的单元格是 NULL
dt query --sql 'SELECT COUNT(*) AS 行数, COUNT("名称_2") AS 有规格, COUNT("数量") AS 有数量 FROM messy'
