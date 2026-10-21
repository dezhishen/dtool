# 场景：财务 / 会计

> 你是这家公司的财务。手上是 `sales.xlsx` 的两张表：`订单`（120 单，含状态）与
> `客户`（15 家，含等级/城市/注册日期）。
> 目标：把「收入」这件事钉死——**哪些能确认、多少要退、账实是否一致、档案是否可信**。

**对 AI 说**：「用 `sales.xlsx` 核对金额是否等于单价 × 数量，按状态和月份出确认收入与
退款表，列出退款金额 Top 的订单并把明细导成 CSV，再交叉核对订单里的客户是否都在档案里。」

**数据源**：[`../../examples/data/sales.xlsx`](../../examples/data/sales.xlsx)。

## 先定口径（财务场景的全部功夫都在这里）

| 指标 | 本场景定义 | 为什么不这么定就错 |
|------|-----------|-------------------|
| 确认收入 | `状态 = '已完成'` 的金额 | 退款要冲回、配送中未签收，都不算 |
| 待确认收入 | `状态 = '配送中'` 的金额 | 已发货未签收，下月可能变退款 |
| 退款额 | `状态 = '已退款'` 的金额 | 与「销售额」互斥，不能相加 |
| 退款率 | 退款单数 ÷ 总单数（**金额口径另算**） | 单量口径与金额口径常常差一倍 |
| 账实一致 | `ABS(单价 × 数量 − 金额) ≤ 0.01` | 浮点误差留 1 分容差，别用 `=` |

## 准备

```bash
mkdir -p /tmp/fin && cd /tmp/fin
cp /path/to/dtool/examples/data/sales.xlsx .
dtool convert --input sales.xlsx --sheet 订单 --name orders
dtool convert --input sales.xlsx --sheet 客户 --name customers
dtool datasets show orders
```

> 下文用 `--rows` 展示取值（**仅 `privacy: normal` 可用**）；默认强隐私去掉它即可。

## ★ 三种口径一眼分开

```bash
dtool query --rows --sql "
  SELECT 状态, COUNT(*) AS 单数, ROUND(SUM(金额)) AS 金额
  FROM orders GROUP BY 1 ORDER BY 金额 DESC"
```

```json
{"状态":"已完成","单数":85,"金额":392022}
{"状态":"配送中","单数":22,"金额":101883}
{"状态":"已退款","单数":13,"金额":93534}
```

**读法**：签约额 58.7 万，确认收入只有 **39.2 万**（66.7%），退款 9.35 万（占签约额 15.9%）。
任何直接拿「SUM(金额)」当收入的报表，都会高估 50%。配送中 10.2 万是下个月的**风险敞口**。

## ★ 退款明细：先看金额，再看原因

```bash
dtool query --rows --sql "
  SELECT 订单号, 日期, 产品, 数量, ROUND(金额) AS 金额
  FROM orders WHERE 状态 = '已退款' ORDER BY 金额 DESC LIMIT 3"

dtool query --format csv --output out/退款明细.csv --sql "
  SELECT 订单号, 日期, 客户ID, 产品, 数量, 单价, 金额
  FROM orders WHERE 状态 = '已退款' ORDER BY 金额 DESC"
```

```json
{"订单号":"OD2026-0024","日期":"2026-02-05","产品":"笔记本","数量":6,"金额":34194}
{"订单号":"OD2026-0042","日期":"2026-03-04","产品":"笔记本","数量":5,"金额":26996}
{"订单号":"OD2026-0048","日期":"2026-03-13","产品":"笔记本","数量":2,"金额":11998}
```

**读法**：退款前三名**全是笔记本**（合计 7.3 万，占退款总额 78%）。这不是随机退货，
是品类问题——先查笔记本的批次/物流，再谈售后退款流程。

## ★★ 月度：确认收入与退款分列

```bash
dtool query --rows --sql "
  SELECT substr(日期,1,7) AS 月份,
         ROUND(SUM(CASE WHEN 状态 = '已退款' THEN 0 ELSE 金额 END)) AS 确认收入,
         ROUND(SUM(CASE WHEN 状态 = '已退款' THEN 金额 ELSE 0 END)) AS 退款金额
  FROM orders GROUP BY 1 ORDER BY 1"
```

```json
{"月份":"2026-01","确认收入":75291,"退款金额":1922}
{"月份":"2026-02","确认收入":75061,"退款金额":34194}
{"月份":"2026-03","确认收入":118120,"退款金额":40293}
{"月份":"2026-04","确认收入":71890,"退款金额":1994}
{"月份":"2026-05","确认收入":84549,"退款金额":3792}
{"月份":"2026-06","确认收入":68994,"退款金额":11338}
```

**读法**：3 月确认收入最高（11.8 万），**退款也是最高（4.0 万）**——增长与退货同时发生，
典型「冲量后反噬」。2 月退款 3.4 万几乎等于那一单 OD2026-0024 的金额：
退款是**事件驱动**（大单退货），不是均匀发生，所以看月度均值没有意义，要看单笔分布。

## ★★ 账实核对：单价 × 数量 是否等于金额

```bash
dtool query --rows --sql "
  SELECT COUNT(*) AS 总单数,
         SUM(CASE WHEN ABS(ROUND(单价*数量,2) - 金额) > 0.01 THEN 1 ELSE 0 END) AS 不一致
  FROM orders"

dtool query --rows --sql "
  SELECT ROUND(MAX(ABS(ROUND(单价*数量,2) - 金额)),2) AS 最大偏差 FROM orders"
```

```json
{"总单数":120,"不一致":0}
{"最大偏差":0}
```

**读法**：120 单零偏差，最大偏差 0 元——说明金额字段是算出来的，**没有再打折/附加费**。
反过来，如果这里出现几百块偏差，那才是需要问业务的地方（折扣、赠品、运费）。

## ★★ 退款率：按单量还是按金额

```bash
dtool query --rows --sql "
  SELECT 产品, COUNT(*) AS 单数,
         SUM(CASE WHEN 状态 = '已退款' THEN 1 ELSE 0 END) AS 退款单,
         ROUND(100.0*SUM(CASE WHEN 状态 = '已退款' THEN 1 ELSE 0 END)/COUNT(*),1) AS 退款率
  FROM orders GROUP BY 1 ORDER BY 退款率 DESC"
```

```json
{"产品":"笔记本","单数":18,"退款单":3,"退款率":16.7}
{"产品":"鼠标","单数":30,"退款单":4,"退款率":13.3}
{"产品":"键盘","单数":20,"退款单":2,"退款率":10}
{"产品":"显示器","单数":25,"退款单":2,"退款率":8}
{"产品":"耳机","单数":27,"退款单":2,"退款率":7.4}
```

**读法**：整体单量退款率 10.8%，但**金额退款率 15.9%**——差在笔记本：它单量少（18 单）
金额大，所以「按单量看没那么糟」的结论是错的。给售后定指标要用**金额口径**。

## ★★★ 客户档案交叉核对

```bash
dtool query --rows --sql "
  SELECT COUNT(*) AS 订单里的客户ID, COUNT(c.客户ID) AS 能对上的
  FROM orders o LEFT JOIN customers c ON o.客户ID = c.客户ID"

dtool query --rows --sql "
  SELECT c.客户名称, c.等级, c.城市
  FROM customers c LEFT JOIN orders o ON o.客户ID = c.客户ID AND o.状态 <> '已退款'
  GROUP BY 1,2,3 HAVING COUNT(o.订单号) = 0"
```

```json
{"订单里的客户ID":120,"能对上的":120}
```

第二条返回空——**15 家客户今年都下过单，没有沉睡客户**。

**读法**：这两条查的是两种错误：前者是「订单里有幽灵客户」（会导致对账对不上），
后者是「档案里有僵尸客户」（会导致续约预测虚高）。本数据两者都为 0，
说明数据干净——但流程要留着，因为接真实数据时它一定会报出东西。

## ★★★ 一键对账表（给老板/审计）

```bash
dtool query --format xlsx --output out/月度对账表.xlsx --sql "
  SELECT substr(日期,1,7) AS 月份,
         ROUND(SUM(金额)) AS 签约额,
         ROUND(SUM(CASE WHEN 状态 = '已完成' THEN 金额 ELSE 0 END)) AS 确认收入,
         ROUND(SUM(CASE WHEN 状态 = '配送中' THEN 金额 ELSE 0 END)) AS 待确认,
         ROUND(SUM(CASE WHEN 状态 = '已退款' THEN 金额 ELSE 0 END)) AS 退款
  FROM orders GROUP BY 1 ORDER BY 1"
```

## 对账结论（可直接抄）

1. 签约 58.7 万 → 确认收入 **39.2 万**（66.7%）→ 退款 9.35 万（15.9%）→ 待确认 10.2 万（17.3%）。
2. 退款集中在**笔记本**（7.3 万，占退款 78%），且集中在 2–3 月两笔大单，属事件驱动，
   要按单笔追责而不是看月度均值。
3. 账实核对 120/120 一致，最大偏差 0 元；客户档案 120/120 能对上，无沉睡客户——
   **数据可信，结论可用**。
4. 售后指标改用**金额退款率**：单量 10.8% 会低估问题，金额 15.9% 才反映真实损失。

## 常见坑

- **`= ]` 的容差**：金额是浮点，判等要用 `ABS(a-b) > 0.01`；直接 `=` 会漏判。
- **`CASE WHEN` 分列时别忘了 `ELSE 0`**：漏了会得到 NULL，`SUM` 之后整行变 NULL。
- **退款率的分母**：按单量还是按金额，结论可能差一倍，写进 `--notes`。
- **`配送中` 的处理**：本月不算收入，但要作为「下月风险敞口」单独报，别静默丢掉。
- **`substr(日期,1,7)`** 要求日期是 `YYYY-MM-DD` 文本（转换时已统一），否则先 `substr(CAST(...))`。
- **强隐私下 `query --format csv` 必须带 `--output`**，否则直接 code 6。

## 完整命令清单

```bash
dtool convert --input sales.xlsx --sheet 订单 --name orders
dtool convert --input sales.xlsx --sheet 客户 --name customers

dtool query --sql "SELECT 状态, COUNT(*) 单数, ROUND(SUM(金额)) 金额 FROM orders GROUP BY 1 ORDER BY 3 DESC"
dtool query --sql "SELECT 产品, COUNT(*) 单数, SUM(CASE WHEN 状态='已退款' THEN 1 ELSE 0 END) 退款单 FROM orders GROUP BY 1 ORDER BY 3 DESC"
dtool query --sql "SELECT COUNT(*) 总单数, SUM(CASE WHEN ABS(ROUND(单价*数量,2)-金额) > 0.01 THEN 1 ELSE 0 END) 不一致 FROM orders"
dtool query --sql "SELECT substr(日期,1,7) 月份, ROUND(SUM(CASE WHEN 状态='已退款' THEN 0 ELSE 金额 END)) 确认收入, ROUND(SUM(CASE WHEN 状态='已退款' THEN 金额 ELSE 0 END)) 退款 FROM orders GROUP BY 1 ORDER BY 1"
dtool query --format xlsx --output out/月度对账表.xlsx --sql "SELECT substr(日期,1,7) 月份, ROUND(SUM(金额)) 签约额 FROM orders GROUP BY 1 ORDER BY 1"
```
