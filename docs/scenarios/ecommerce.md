# 场景：电商 / 仓储运营

> 你手上是订单明细（`sales.xlsx` 的 `订单` 表，含 产品 / 数量 / 金额 / 状态 / 区域 / 客户ID）。
> 目标：找爆款、看退款、算复购、给各区域排座次。

## 准备

```bash
mkdir -p /tmp/demo && cd /tmp/demo
cp /path/to/dtool/examples/data/sales.xlsx .
dtool convert --input sales.xlsx --sheet 订单 --name orders
dtool convert --input sales.xlsx --sheet 客户 --name customers
```

## ★ SKU 销量排行

```bash
dtool query --sql "SELECT 产品, SUM(数量) AS 销量, ROUND(SUM(金额),2) AS 销售额
  FROM orders GROUP BY 1 ORDER BY 销量 DESC"
```

```json
{"产品":"显示器","销量":147,"销售额":180950.7}
{"产品":"鼠标","销量":128,"销售额":15718.65}
{"产品":"笔记本","销量":55,"销售额":314647.55}
```

销量第一不等于销售额第一 —— 两个指标一起看才知道该补哪个货。

## ★ 区域 × 品类 交叉表

```bash
dtool query --sql "SELECT 区域,
    SUM(CASE WHEN 产品 = '笔记本' THEN 数量 END) AS 笔记本,
    SUM(CASE WHEN 产品 = '显示器' THEN 数量 END) AS 显示器,
    SUM(CASE WHEN 产品 = '键盘' THEN 数量 END) AS 键盘,
    SUM(CASE WHEN 产品 = '鼠标' THEN 数量 END) AS 鼠标,
    SUM(CASE WHEN 产品 = '耳机' THEN 数量 END) AS 耳机
  FROM orders GROUP BY 1"
```

这里 `ELSE` 故意省略：没匹配到就是 `NULL`，哪块是空白一眼可见。

## ★★ 各产品退款率

```bash
dtool query --sql "SELECT 产品, COUNT(*) AS 单数,
    ROUND(100.0*SUM(CASE WHEN 状态 = '已退款' THEN 1 ELSE 0 END)/COUNT(*),1) AS 退款率
  FROM orders GROUP BY 1 ORDER BY 退款率 DESC"
```

```json
{"产品":"笔记本","单数":18,"退款率":16.7}
{"产品":"鼠标","单数":30,"退款率":13.3}
```

## ★★ 新客首单月份（CTE + MIN）

```bash
dtool query --sql "WITH t AS (SELECT 客户ID, MIN(日期) AS 首单日期 FROM orders GROUP BY 1)
  SELECT strftime('%Y-%m', 首单日期) AS 月份, COUNT(*) AS 新客数 FROM t GROUP BY 1 ORDER BY 1"
```

## ★★★ 复购结构：一次性 vs 复购

```bash
dtool query --sql "SELECT CASE WHEN 单数 >= 2 THEN '复购客户' ELSE '一次性客户' END AS 类型,
    COUNT(*) AS 客户数, ROUND(AVG(总额),2) AS 平均消费
  FROM (SELECT 客户ID, COUNT(*) AS 单数, SUM(金额) AS 总额 FROM orders GROUP BY 1)
  GROUP BY 1"
```

先按客户聚合，再对聚合结果分组 —— 「先明细后汇总」是运营分析的常见两层结构。

## ★★★ 每个区域的 TOP2 品类

```bash
dtool query --sql "SELECT 区域, 产品, 销量 FROM (
    SELECT 区域, 产品, SUM(数量) AS 销量,
           RANK() OVER (PARTITION BY 区域 ORDER BY SUM(数量) DESC) AS 区域排名
    FROM orders GROUP BY 1,2) WHERE 区域排名 <= 2 ORDER BY 区域, 销量 DESC"
```

窗口函数可以和聚合写在同一个查询里：先 `GROUP BY 区域,产品` 得到销量，再在结果上按区域排名。

## 常见坑

| 现象 | 处理 |
|------|------|
| 销量/销售额单位混在一起 | 明确别名（`AS 销量` / `AS 销售额`），别都叫「数量」 |
| `SUM(CASE WHEN ... THEN 数量 END)` 出 NULL | 这是有意的空档标记；要 0 就补 `ELSE 0` |
| 同一客户在多个区域被重复计数 | 用 `COUNT(DISTINCT 客户ID)` |
| `WHERE 产品 = "键盘"` 报 no such column | 字符串值用**单引号**：`WHERE 产品 = '键盘'`；双引号是标识符 |

相关：[连表查询规则](../../skills.md) · [场景总览](README.md)
