# 场景：财务 / 会计

> 你手上是销售流水（`sales.xlsx` 的 `订单` 表：订单号、日期、客户ID、产品、数量、单价、金额、状态）。
> 目标：核对收入口径、挑出异常账、做两张表的交叉核对。

## 准备

```bash
mkdir -p /tmp/demo && cd /tmp/demo
cp /path/to/dtool/examples/data/sales.xlsx .
dtool convert --input sales.xlsx --sheet 订单 --name orders
dtool convert --input sales.xlsx --sheet 客户 --name customers
```

## ★ 口径核对：按状态汇总

```bash
dtool query --sql "SELECT 状态, COUNT(*) AS 单数, ROUND(SUM(金额),2) AS 金额
  FROM orders GROUP BY 1 ORDER BY 金额 DESC"
```

```json
{"状态":"已完成","单数":85,"金额":392021.7}
{"状态":"配送中","单数":22,"金额":101882.5}
{"状态":"已退款","单数":13,"金额":93533.6}
```

一眼看出待确认收入（配送中）与退款规模的量级。

## ★ 挑出退款明细（导 CSV 给审计）

```bash
dtool query --format csv --output 退款明细.csv --sql "SELECT 订单号, 日期, 客户ID, 产品, 金额
  FROM orders WHERE 状态 = '已退款' ORDER BY 金额 DESC"
```

## ★★ 月度确认收入与退款（`CASE WHEN` 分列）

```bash
dtool query --sql "SELECT strftime('%Y-%m', 日期) AS 月份,
    ROUND(SUM(CASE WHEN 状态 = '已完成' THEN 金额 ELSE 0 END),2) AS 确认收入,
    ROUND(SUM(CASE WHEN 状态 = '已退款' THEN 金额 ELSE 0 END),2) AS 退款金额
  FROM orders GROUP BY 1 ORDER BY 1"
```

## ★★ 账实核对：单价 × 数量 = 金额？

```bash
dtool query --sql "SELECT 订单号, 数量, 单价, 金额, ROUND(单价*数量,2) AS 应有金额
  FROM orders WHERE ABS(单价*数量 - 金额) > 0.01"
```

返回 `row_count: 0` 就是账平。用 SQL 找异常行是财务最常用的姿势，换成「汇率错误」「税费不符」同理。

## ★★ 退款率按产品

```bash
dtool query --sql "SELECT 产品, COUNT(*) AS 单数,
    ROUND(100.0*SUM(CASE WHEN 状态 = '已退款' THEN 1 ELSE 0 END)/COUNT(*),1) AS 退款率
  FROM orders GROUP BY 1 ORDER BY 退款率 DESC"
```

```json
{"产品":"笔记本","单数":18,"退款率":16.7}
{"产品":"鼠标","单数":30,"退款率":13.3}
```

## ★★★ 交叉核对两张表：订单里的客户在档案里吗？

```bash
dtool query --sql "SELECT DISTINCT o.客户ID
  FROM orders o LEFT JOIN customers c ON o.客户ID = c.客户ID
  WHERE c.客户ID IS NULL"
```

`LEFT JOIN ... WHERE 右表.键 IS NULL` 是经典的「找孤儿记录」写法：有结果说明主数据缺失，
没结果（`row_count: 0`）说明两边对得上。同一招可以查「有订单无回款」「有发票无合同」。

## ★★★ 一键出对账表（查询 → Excel）

```bash
dtool query --format xlsx --output 对账表.xlsx --sql "SELECT strftime('%Y-%m', 日期) AS 月份,
    COUNT(*) AS 单数,
    ROUND(SUM(CASE WHEN 状态 <> '已退款' THEN 金额 ELSE 0 END),2) AS 净收入,
    ROUND(SUM(CASE WHEN 状态 = '已退款' THEN 金额 ELSE 0 END),2) AS 退款
  FROM orders GROUP BY 1 ORDER BY 1"
```

产出的 xlsx 可直接贴进月结底稿；需要留审计痕迹时给命令加 `--notes "月结底稿口径"`。

## 常见坑

| 现象 | 处理 |
|------|------|
| 金额出现浮点尾差（`138369.05000000002`） | 用 `ROUND(x,2)` 收口；比较用 `ABS(a-b) > 0.01`，不要用 `=` |
| `状态 = "已退款"` 查不到 | SQL 字符串常量用**单引号**，双引号是标识符 |
| 想筛空值却筛不出来 | 用 `WHERE 列 IS NULL`，`= NULL` 永远不成立 |
| 结果被 `--max-rows` 拦下 | 默认上限 10000 行且**报错**而非截断，先聚合或加 `LIMIT` |

相关：[SQL 规则](../../skills.md) · [场景总览](README.md)
