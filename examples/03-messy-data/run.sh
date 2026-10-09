#!/usr/bin/env bash
# 示例 3：脏数据 —— 空表头/重复表头/空行/合并单元格/前导零/混合类型，看 warnings 与 Schema 如何处理
source "$(dirname "$0")/../lib.sh"
demo_init 03-messy-data

# convert 的 warnings 会列出自动做了哪些修正（空表头 → col_2，重复表头 → 名称_2，合并单元格提示）
dt convert --input "$DATA/messy.xlsx" --name messy
dt datasets show messy

# "金额"含 N/A，整列被判为 string；需要计算时显式转换（NULLIF 把 N/A 变成 NULL，而不是被转成 0）
dt query --sql 'SELECT "编号", "手机号", CAST(NULLIF("金额", '"'N/A'"') AS REAL) AS 金额 FROM messy ORDER BY 1'
# 手机号保留了前导零；缺失的单元格是 NULL
dt query --sql 'SELECT COUNT(*) AS 行数, COUNT("名称_2") AS 有规格, COUNT("数量") AS 有数量 FROM messy'
