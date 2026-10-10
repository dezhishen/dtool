# 场景：销售 / 业务运营

> 你手上有一份订单明细（`sales.xlsx` 的 `订单` 表，120 行）和一份客户档案（`客户` 表，15 行）。
> 目标：从「各区卖了多少」一路做到「哪些客户在贡献增长」，并把结果直接发给老板。

**对 AI 说**：「用 sales.xlsx 的订单表按区域汇总销售额并出柱状图，再给我金额 Top 5 客户的 Excel。」

## 准备

```bash
mkdir -p /tmp/demo && cd /tmp/demo
cp /path/to/dtool/examples/data/sales.xlsx .
dtool convert --input sales.xlsx --sheet 订单 --name orders
dtool convert --input sales.xlsx --sheet 客户 --name customers
```

## ★ 入门：一张表看清各区域业绩

**对 AI 说**：「把 sales.xlsx 的订单按区域汇总单数和金额」

**要解决的问题**：这个季度哪个区域卖得最好？

```bash
dtool query --sql "SELECT 区域, COUNT(*) AS 单数, ROUND(SUM(金额),2) AS 销售额
  FROM orders GROUP BY 1 ORDER BY 销售额 DESC"
```

```json
{"区域":"西南","单数":27,"销售额":187706.0}
{"区域":"华东","单数":40,"销售额":174251.1}
{"区域":"华北","单数":34,"销售额":138859.7}
{"区域":"华南","单数":19,"销售额":86621.0}
```

- 表名就是数据集名（`orders`），不用写文件路径。
- `GROUP BY 1` 是「按第 1 列分组」的简写；`ORDER BY 销售额` 直接引用别名。
- 金额是浮点，`ROUND(x,2)` 收口，免得出现 `187706.00000000003` 这种尾差。

## ★ 入门：把结果变成柱状图

**对 AI 说**：「把这个结果画成柱状图，标题「各区域销售额」」

```bash
dtool visualize --input latest:query --type bar --x 区域 --y 销售额 \
  --title "各区域销售额" --output 区域销售额.png
```

`latest:query` 指**最近一次查询**，所以要紧跟在上面的 `query` 之后执行；顺序不确定时先
`dtool actions list` 拿到 action id，再用 `--input action:<id>`。中文标题会自动挑系统中文字体。

## ★★ 进阶：把客户档案接进来（JOIN）

**对 AI 说**：「接上客户档案，按消费金额排 Top 10 客户」

```bash
dtool query --sql "SELECT c.客户名称, c.等级, c.城市, COUNT(*) AS 单数,
    ROUND(SUM(o.金额),2) AS 总额
  FROM orders o JOIN customers c ON o.客户ID = c.客户ID
  GROUP BY 1,2,3 ORDER BY 总额 DESC LIMIT 5"
```

```json
{"客户名称":"远航物流","等级":"银牌","城市":"天津","单数":10,"总额":83077.25}
{"客户名称":"天成建设","等级":"银牌","城市":"广州","单数":10,"总额":75199.9}
{"客户名称":"星辰科技","等级":"普通","城市":"成都","单数":11,"总额":55945.9}
```

要点：

- 两个数据源在同一个内存 SQLite 里，各自是**一张表**，可以随便 JOIN。
- `客户ID` 列是 `C001` 这类**文本**（带字母/前导零），两边类型一致，直接等值比较即可。
- `LEFT JOIN` 时未匹配的一侧是 NULL，数个数要用 `COUNT(o.订单号)` 而不是 `COUNT(*)`。

## ★★ 进阶：导出 Excel 交给业务方

**对 AI 说**：「导成 Excel，我要发给业务方」

```bash
dtool query --format xlsx --output 客户销售.xlsx --sql "SELECT c.客户名称, c.等级,
    COUNT(*) AS 单数, ROUND(SUM(o.金额),2) AS 总额
  FROM orders o JOIN customers c ON o.客户ID = c.客户ID
  GROUP BY 1,2 ORDER BY 总额 DESC"
```

`--format` 还支持 `csv` / `markdown` / `table`；不写 `--output` 时打印到终端。

## ★★★ 综合：客户贡献占比（CTE + 子查询）

**对 AI 说**：「算一下 Top 5 客户占总额的比例」

```bash
dtool query --sql "WITH cust AS (
    SELECT c.客户名称 AS 客户, ROUND(SUM(o.金额),2) AS 总额
    FROM orders o JOIN customers c ON o.客户ID = c.客户ID GROUP BY 1)
  SELECT 客户, 总额, ROUND(100.0*总额/(SELECT SUM(总额) FROM cust),2) AS 占比
  FROM cust ORDER BY 总额 DESC LIMIT 5"
```

输出 top5 客户及其占比，可直接进周报。CTE、子查询、`UNION`、窗口函数都支持，但沙箱只允许
**单条 `SELECT`/`WITH`**。

## ★★★ 综合：月度趋势 + 留痕

**对 AI 说**：「按月看销售额趋势出折线图，这次分析标成「月报」」

```bash
dtool query --tags 月报,销售 --notes "月度复盘：各区域销售额" --sql "SELECT
    strftime('%Y-%m', 日期) AS 月份, ROUND(SUM(金额),2) AS 销售额
  FROM orders GROUP BY 1 ORDER BY 1"

dtool visualize --input latest:query --type line --x 月份 --y 销售额 \
  --title "月度销售额" --output 月度趋势.png

dtool actions list --limit 3
```

`--tags` / `--notes` 会写进 Action 元数据，之后 `dtool actions list` 就能按「月报」找回这次分析，
`dtool actions trace <id>` 还能看到它派生出哪张图。

## 常见坑

| 现象 | 原因 / 处理 |
|------|-------------|
| `no such column: o.订单ID` | 列名写错，先 `dtool datasets show orders` 核对 |
| 出图报 `field not found` | `latest:query` 取到的是**另一次**查询，改用 `--input action:<id>` |
| `result exceeds --max-rows` | 结果超过 10000 行，加 `LIMIT` 或调大 `--max-rows` |
| 中文显示成方框 | 指定中文字体：`--font /path/to/字体.ttf`，或在配置文件里设 `font:` |

相关：[连表查询规则](../../skills/dtool/SKILL.md) · [场景总览](README.md)
