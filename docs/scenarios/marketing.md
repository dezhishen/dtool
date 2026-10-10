# 场景：市场 / 增长

> 你手上是订单明细 + 客户档案（等级：普通 / 银牌 / 金牌）。
> 目标：看趋势、看结构、算环比、评估新客获取与客单价。

**对 AI 说**：「用 sales.xlsx 看月度销售额趋势和环比，再统计新客首单月份与客户城市分布，各出一张图。」

## 准备

```bash
mkdir -p /tmp/demo && cd /tmp/demo
cp /path/to/dtool/examples/data/sales.xlsx .
dtool convert --input sales.xlsx --sheet 订单 --name orders
dtool convert --input sales.xlsx --sheet 客户 --name customers
```

## ★ 月度趋势：一条 SQL + 一张折线图

**对 AI 说**：「按月看销售额趋势并出折线图」

```bash
dtool query --sql "SELECT strftime('%Y-%m', 日期) AS 月份, COUNT(*) AS 单数,
    ROUND(SUM(金额),2) AS 销售额
  FROM orders GROUP BY 1 ORDER BY 1"

dtool visualize --input latest:query --type line --x 月份 --y 销售额 \
  --title "月度销售额" --output 月度趋势.png
```

```json
{"月份":"2026-01","单数":21,"销售额":77213.2}
{"月份":"2026-03","单数":21,"销售额":158412.45}
```

`strftime` 把 `YYYY-MM-DD` 文本日期裁到月；按 `%Y-%m` 分组即可得到月度序列。

## ★ 客户结构：城市 × 等级

**对 AI 说**：「按城市和等级看客户数和销售额」

```bash
dtool query --sql "SELECT c.城市, c.等级, COUNT(DISTINCT o.客户ID) AS 客户数,
    ROUND(SUM(o.金额),2) AS 销售额
  FROM orders o JOIN customers c ON o.客户ID = c.客户ID
  GROUP BY 1,2 ORDER BY 销售额 DESC"
```

`COUNT(DISTINCT 客户ID)` 去重计数，避免一个客户被重复算成多个。

## ★★ 环比增长（`LAG` 窗口函数）

**对 AI 说**：「算一下销售额的月度环比增长」

```bash
dtool query --sql "WITH m AS (SELECT strftime('%Y-%m', 日期) AS 月份,
      ROUND(SUM(金额),2) AS 销售额 FROM orders GROUP BY 1)
  SELECT 月份, 销售额, LAG(销售额) OVER (ORDER BY 月份) AS 上月,
    ROUND(100.0*(销售额 - LAG(销售额) OVER (ORDER BY 月份))
          / LAG(销售额) OVER (ORDER BY 月份), 1) AS 环比
  FROM m"
```

第一个月没有上月，`上月` / `环比` 为 `NULL`，属正常。想看同比就按月份再关联一份去年数据。

## ★★ 等级 × 客单价

**对 AI 说**：「各等级客户的客单价是多少」

```bash
dtool query --sql "SELECT c.等级, COUNT(DISTINCT o.客户ID) AS 客户数, COUNT(*) AS 单数,
    ROUND(SUM(o.金额)/COUNT(*),2) AS 客单价
  FROM orders o JOIN customers c ON o.客户ID = c.客户ID
  GROUP BY 1 ORDER BY 客单价 DESC"
```

```json
{"等级":"银牌","客单价":5943.6}
{"等级":"普通","客单价":4610.11}
{"等级":"金牌","客单价":3445.86}
```

示例数据里金牌客单价反而最低 —— 这类「反直觉」正是分析要回答的问题（金牌客户在冲量、还是品类结构不同？）。

## ★★★ 新客获取节奏 + 城市分布图

**对 AI 说**：「新客获取的月度节奏和客户城市分布，各出一张图」

```bash
# 每位客户的首单月份 -> 每月新增客户数
dtool query --sql "WITH first_order AS (
    SELECT 客户ID, MIN(日期) AS 首单日期 FROM orders GROUP BY 1)
  SELECT strftime('%Y-%m', 首单日期) AS 月份, COUNT(*) AS 新客数
  FROM first_order GROUP BY 1 ORDER BY 1"

dtool visualize --input latest:query --type bar --x 月份 --y 新客数 \
  --title "每月新增客户" --output 新客获取.png

# 客户来自哪些城市
dtool query --sql "SELECT c.城市, COUNT(DISTINCT o.客户ID) AS 客户数
  FROM orders o JOIN customers c ON o.客户ID = c.客户ID
  GROUP BY 1 ORDER BY 客户数 DESC"

dtool visualize --input latest:query --type pie --x 城市 --y 客户数 \
  --title "客户城市分布" --output 城市分布.png
```

两张图 + 两段结论，就是一次完整的月度增长复盘。每条命令都会留痕，事后可以
`dtool actions list --limit 5` 回看当时的口径。

## 常见坑

| 现象 | 处理 |
|------|------|
| 折线的 X 轴顺序乱 | 一定要 `ORDER BY 1`；字符串月份按字典序排正好是时间序 |
| 除法结果为 0 | 整数除法：写成 `100.0*a/b` 或 `1.0*a/b` |
| 第二张图取到第一张的数据 | 每张图前面紧跟它自己的 `query`，或改用 `--input action:<id>` |
| 环比出现 `null` | 首月无上月，用 `COALESCE(环比, 0)` 或直接忽略 |

相关：[场景总览](README.md) · [SQL 规则](../../skills/dtool/SKILL.md)
