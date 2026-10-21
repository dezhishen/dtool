# 场景：销售 / 电商 / 增长（同一份订单数据的三个视角）

> 你是区域销售负责人，也兼管商品与增长。手上是 `sales.xlsx`：`订单` 表 120 单
> （2026-01～06，四个区域、5 个产品）和 `客户` 表 15 家（等级、城市、注册日期）。
> 目标：回答六件事——**各区域卖了多少、增长从哪来、客户依赖多重、哪个 SKU 在挣钱、
> 复购能不能算、金牌客户为什么反而便宜**。
>
> 原来这里有三份文档（销售 / 电商运营 / 市场增长），用的是同一份数据、同一套口径，
> 拆成三份只会让口径各说一套——**已合并**，商品与增长视角见 ★★ / ★★★ 两节。

**对 AI 说**：「用 `sales.xlsx` 按区域汇总销售额并出柱状图，再做区域 × 月份的多系列趋势，
最后给我金额 Top 5 客户的 Excel，口径写进 notes。」

**数据源**：[`../../examples/data/sales.xlsx`](../../examples/data/sales.xlsx)（两个工作表：`订单` / `客户`）。

## 先定口径（这四条不定，数字一定吵）

| 指标 | 本场景定义 | 为什么 |
|------|-----------|--------|
| 销售额 | `SUM(金额)`，**排除 `已退款`** | 退款不是收入；含退款会把业绩虚增 15.9%（9.35 万） |
| 待确认收入 | `状态 = '配送中'` 的金额 | 已发货未签收，财务上还不算收入 |
| 客单价 | `AVG(金额)`，同样排除退款 | 区域之间比「单笔值多少钱」，而不是比单量 |
| 客户集中度 | 金额 Top3 客户合计 ÷ 全部销售额 | 判断「丢一个大客户会不会伤筋动骨」 |

## 准备

```bash
mkdir -p /tmp/demo && cd /tmp/demo
cp /path/to/dtool/examples/data/sales.xlsx .

dtool convert --input sales.xlsx --sheet 订单 --name orders
dtool convert --input sales.xlsx --sheet 客户 --name customers
dtool datasets show orders          # 先看列名/类型，别猜
```

> 下文展示取值用了 `--rows`（**只在 `privacy: normal` 下可用**）；默认强隐私去掉它即可，
> 结果一样会写进 `outputs/queries/<action_id>/result.json`。

## ★ 各区域业绩：先看总量，再看质量

**对 AI 说**：「按区域汇总单数和销售额，排除退款」

```bash
dtool query --rows --sql "
  SELECT 区域, COUNT(*) AS 单数, ROUND(SUM(金额)) AS 销售额
  FROM orders WHERE 状态 <> '已退款'
  GROUP BY 1 ORDER BY 销售额 DESC"
```

```json
{"区域":"西南","单数":25,"销售额":183415}
{"区域":"华东","单数":36,"销售额":137816}
{"区域":"华北","单数":30,"销售额":99629}
{"区域":"华南","单数":16,"销售额":73045}
```

**读法**：西南 25 单做到 18.3 万——单笔最贵；华东 36 单只有 13.8 万，是西南的 75%。
这不是「谁第一」的问题，而是**两种打法**：西南靠客单价，华东靠单量，而且华东的单还不值钱。

## ★ 把结论变成图：一张图讲完

**对 AI 说**：「把这个结果画成柱状图，我要放进周报」

```bash
dtool query --sql "
  SELECT 区域, ROUND(SUM(金额)) AS 销售额 FROM orders
  WHERE 状态 <> '已退款' GROUP BY 1 ORDER BY 2 DESC" 
dtool visualize --input latest:query --type bar --x 区域 --y 销售额 \
  --title "各区域销售额（2026 H1）" --output out/区域销售额.png
```

产物落在 `out/区域销售额.png`；stdout 只给清单（强隐私下把 `artifacts[].abs_path` 当附件发）。

**读法**：出图前先跑 `query`，图就直接引用 `latest:query`——数据与图的关系被记进 Action，
事后能答「这张图怎么来的」。

## ★★ 增长从哪来：区域 × 月份

**对 AI 说**：「各区域按月的销售额，出多系列折线，我要看谁在掉」

```bash
dtool query --sql "
  SELECT 区域, substr(日期,1,7) AS 月份, ROUND(SUM(金额)) AS 销售额
  FROM orders WHERE 状态 <> '已退款'
  GROUP BY 1,2 ORDER BY 1,2"
dtool visualize --input latest:query --type line --x 月份 --y 销售额 \
  --group-by 区域 --title "区域月度销售额" --output out/区域月度.png
```

```json
{"区域":"华东","月份":"2026-01","销售额":48702}
{"区域":"华东","月份":"2026-02","销售额":23784}
{"区域":"华东","月份":"2026-03","销售额":8031}
{"区域":"华东","月份":"2026-04","销售额":13518}
{"区域":"华东","月份":"2026-05","销售额":26411}
{"区域":"华东","月份":"2026-06","销售额":17370}
```

**读法**：华东 1 月 4.87 万、3 月只有 0.8 万——**掉了一个数量级**，而全区月度总额看起来还算平
（3 月 11.8 万是全年最高）。总量掩盖了结构问题：3 月的增长来自别的区域，华东在失血。

## ★★ 客户依赖：谁在贡献，谁在流失

**对 AI 说**：「金额 Top 5 客户及其等级，再算一下 Top3 的占比」

```bash
dtool query --rows --sql "
  SELECT c.客户名称, c.等级, COUNT(*) AS 单数, ROUND(SUM(o.金额)) AS 销售额
  FROM orders o JOIN customers c ON o.客户ID = c.客户ID
  WHERE o.状态 <> '已退款'
  GROUP BY 1,2 ORDER BY 销售额 DESC LIMIT 5"

dtool query --rows --sql "
  WITH 客户额 AS (SELECT 客户ID, SUM(金额) AS 额 FROM orders WHERE 状态 <> '已退款' GROUP BY 1),
       排名 AS (SELECT 额, ROW_NUMBER() OVER (ORDER BY 额 DESC) AS r FROM 客户额)
  SELECT ROUND(100.0*SUM(CASE WHEN r <= 3 THEN 额 END)/SUM(额),1) AS Top3占比 FROM 排名"
```

```json
{"客户名称":"天成建设","等级":"银牌","单数":8,"销售额":62604}
{"客户名称":"南山制造","等级":"普通","单数":7,"销售额":53994}
{"客户名称":"迅达快递","等级":"金牌","单数":14,"销售额":53028}
{"客户名称":"远航物流","等级":"银牌","单数":9,"销售额":48883}
{"客户名称":"恒信金融","等级":"银牌","单数":6,"销售额":43335}
```

```json
{"Top3占比":36.5}
```

**读法**：Top3 占 36.5%，集中度还算健康。但 Top5 里有两个信号：
① **金牌客户「迅达快递」单量最多（14 单）却只排第 3**（5.3 万）——单多但单小，
   说明金牌客户在买低客单产品，要么引导升级，要么按实际 GMV 重算等级；
② Top5 里 3 家是银牌、1 家普通，**评级和贡献基本对得上，但金牌名不副实**。

## ★★ 客单价：区域之间的真实差异

```bash
dtool query --rows --sql "
  SELECT 区域, ROUND(AVG(金额)) AS 客单价, COUNT(*) AS 单数
  FROM orders WHERE 状态 <> '已退款' GROUP BY 1 ORDER BY 客单价 DESC"
```

```json
{"区域":"西南","客单价":7337,"单数":25}
{"区域":"华南","客单价":4565,"单数":16}
{"区域":"华东","客单价":3828,"单数":36}
{"区域":"华北","客单价":3321,"单数":30}
```

**读法**：西南客单价是华北的 2.2 倍。复制西南的打法（高客单产品/大单谈判）比在华北
堆单量更划算——这是「同样营收下人力成本差一倍」的根源。

## ★★ 导出：交给业务方的 Excel

```bash
dtool query --format xlsx --output out/客户Top5.xlsx --sql "
  SELECT c.客户名称, c.等级, c.城市, COUNT(*) AS 单数, ROUND(SUM(o.金额)) AS 销售额
  FROM orders o JOIN customers c ON o.客户ID = c.客户ID
  WHERE o.状态 <> '已退款' GROUP BY 1,2,3 ORDER BY 销售额 DESC LIMIT 5"
```

## ★★★ 一条龙 + 留痕

```bash
dtool pipeline --excel sales.xlsx --sheet 订单 \
  --sql "SELECT substr(日期,1,7) AS 月份, ROUND(SUM(金额)) AS 销售额
         FROM data WHERE 状态 <> '已退款' GROUP BY 1 ORDER BY 1" \
  --chart line --x 月份 --y 销售额 --title "2026 H1 月度销售额" \
  --output out/月度销售额.png --tags 周报 --notes "口径：排除已退款"
```

**读法**：`--tags`/`--notes` 把口径写进 Action。下次有人问「这个数含不含退款」，
`dtool actions list --limit 1` 就能自证，不用再翻聊天记录。

## 周报结论（可直接抄）

1. H1 销售额 **49.4 万**（含退款是 58.7 万，虚增 15.9%），交付中 10.2 万未确认。
2. 西南靠客单价（7337 元 / 25 单）、华东靠单量（36 单 / 3828 元）；**华东 3 月环比跌 66%**
   （2.38 万 → 0.80 万）需要单独复盘：是缺货、涨价，还是人走了。
3. Top3 客户占 36.5%，集中度可接受，但**金牌客户贡献排第 5**，评级规则该按 GMV 重算。
4. 退款率 13/120 = 10.8%，笔记本（16.7%）与鼠标（13.3%）最高——先查这两个的售后原因。
5. 商品：销量王是显示器（138 台），利润王是笔记本（24.1 万 / 42 台）；华北的耳机
   （53 件）是唯一「区域特色品类」，备货要按区域分开。
6. 增长：3 月 +57.4% 与 4 月 -39.1% 是同一次大单脉冲，不是趋势；**2 月起没有新客**，
   4–6 月全靠 15 家老客户复购——增长问题的根子在获客，不在转化。

## ★★ 商品视角：SKU 销量与销售额不是一回事

**对 AI 说**：「按产品算销量和销售额，销量高和金额高的分别是谁」

```bash
dtool query --rows --sql "
  SELECT 产品, SUM(数量) AS 销量, ROUND(SUM(金额)) AS 销售额
  FROM orders WHERE 状态 <> '已退款' GROUP BY 1 ORDER BY 销量 DESC"
```

```json
{"产品":"显示器","销量":138,"销售额":169260}
{"产品":"鼠标","销量":109,"销售额":13448}
{"产品":"耳机","销量":109,"销售额":52021}
{"产品":"键盘","销量":61,"销售额":17716}
{"产品":"笔记本","销量":42,"销售额":241460}
```

**读法**：销量王是显示器（138 台），**销售额王是笔记本（24.1 万，销量只有 42 台）**。
鼠标卖了 109 只只有 1.3 万——**补货看销量，赚钱看金额**，两个榜单要一起看，否则会把
钱压在便宜货上。

再看各区域偏好的品类：

```bash
dtool query --rows --sql "
  SELECT 区域, 产品, 销量 FROM (
    SELECT 区域, 产品, SUM(数量) AS 销量,
           RANK() OVER (PARTITION BY 区域 ORDER BY SUM(数量) DESC) AS 区域排名
    FROM orders WHERE 状态 <> '已退款' GROUP BY 1,2)
  WHERE 区域排名 <= 2 ORDER BY 区域, 销量 DESC"
```

```json
{"区域":"华东","产品":"鼠标","销量":41}
{"区域":"华东","产品":"显示器","销量":39}
{"区域":"华北","产品":"耳机","销量":53}
{"区域":"华北","产品":"鼠标","销量":30}
{"区域":"华南","产品":"显示器","销量":42}
{"区域":"华南","产品":"耳机","销量":12}
{"区域":"西南","产品":"显示器","销量":33}
{"区域":"西南","产品":"鼠标","销量":27}
```

**读法**：只有华北把耳机买成了第一品类（53 件），其余三个区域都是显示器/鼠标。
**备货按区域分开做**，全国统一排产会把华北的耳机缺货、其他区域的鼠标压库。

## ★★★ 增长视角：环比与新客

```bash
dtool query --rows --sql "
  WITH m AS (SELECT substr(日期,1,7) AS 月份, ROUND(SUM(金额)) AS 销售额
             FROM orders WHERE 状态 <> '已退款' GROUP BY 1)
  SELECT 月份, 销售额, LAG(销售额) OVER (ORDER BY 月份) AS 上月,
         ROUND(100.0*(销售额 - LAG(销售额) OVER (ORDER BY 月份))
               / LAG(销售额) OVER (ORDER BY 月份), 1) AS 环比
  FROM m"
```

```json
{"月份":"2026-01","销售额":75291,"上月":null,"环比":null}
{"月份":"2026-02","销售额":75061,"上月":75291,"环比":-0.3}
{"月份":"2026-03","销售额":118120,"上月":75061,"环比":57.4}
{"月份":"2026-04","销售额":71890,"上月":118120,"环比":-39.1}
{"月份":"2026-05","销售额":84549,"上月":71890,"环比":17.6}
{"月份":"2026-06","销售额":68994,"上月":84549,"环比":-18.4}
```

**读法**：3 月 +57.4% 是全年最强，4 月 -39.1% 直接吐回去——**这是一次性大单，不是增长**。
环比只看相邻两月，遇到这种脉冲行情要同时看 3 个月移动平均，否则会把波动当趋势。

新客节奏（按每位客户的首单月份）：

```bash
dtool query --rows --sql "
  WITH f AS (SELECT 客户ID, MIN(日期) AS 首单日期
             FROM orders WHERE 状态 <> '已退款' GROUP BY 1)
  SELECT substr(首单日期,1,7) AS 月份, COUNT(*) AS 新客数 FROM f GROUP BY 1 ORDER BY 1"
```

```json
{"月份":"2026-01","新客数":12}
{"月份":"2026-02","新客数":2}
{"月份":"2026-03","新客数":1}
```

**读法**：15 家里 12 家是 1 月首单（存量客户集中在年初签约），**2–3 月后没有新客**。
所以 4–6 月的营收全靠老客户复购——这也是「金牌客户客单价反而最低」的背景：
现有客户在重复买便宜的耗材。

```bash
dtool query --rows --sql "
  SELECT c.等级, COUNT(DISTINCT o.客户ID) AS 客户数, COUNT(*) AS 单数,
         ROUND(AVG(o.金额)) AS 客单价
  FROM orders o JOIN customers c ON o.客户ID = c.客户ID
  WHERE o.状态 <> '已退款' GROUP BY 1 ORDER BY 客单价 DESC"
```

```json
{"等级":"银牌","客户数":6,"单数":38,"客单价":5264}
{"等级":"普通","客户数":7,"单数":49,"客单价":4527}
{"等级":"金牌","客户数":2,"单数":20,"客单价":3601}
```

**读法**：**金牌 2 家客户、客单价最低（3601）**，银牌最高（5264）。等级是按历史累计
给的，不代表现在买得贵——要么重定价，要么承认金牌是高复购低毛利客户。

**关于复购率**：本数据 15 家客户**全部**下过 2 单以上（平均 7 单），
「一次性 vs 复购」分组只会得到一行结果，**没有区分度**。这不是算不出来，而是数据里
没有一次性客户——真实业务里要按**窗口**算（如「90 天内是否二次下单」），
否则结论必然虚高。这也是为什么这一条只写成坑，不写成 recipe。

## 常见坑

- **别忘 `WHERE 状态 <> '已退款'`**：漏一次，所有结论都被退货污染。
- **`配送中` 不是收入**：要现金流口径就单独算，别混进「已完成」。
- **`客户ID` 是文本**（`C001` 形式，有前导零）：JOIN 两边都当字符串；类型不一致时 `CAST(x AS TEXT)`。
- **`substr(日期,1,7)` 依赖日期是 ISO 文本**：转换时日期列已统一成 `YYYY-MM-DD`，可以直接切。
- **`ORDER BY 销售额` 用的是别名**：SQLite 与 DuckDB 都支持，但如果写了 `ORDER BY SUM(金额)`，
  去掉退款条件后记得同步改，否则排序与筛选口径不一致。
- **强隐私下 `--rows` 会被拒（code 6）**：Agent 侧只交付产物路径，不复述取值。
- **`SUM(CASE WHEN ... THEN 数量 END)` 出 NULL**：这是有意的「空档标记」；要 0 就补
  `ELSE 0`，但别指望用它区分「没卖」和「卖完了」。
- **复购要带窗口**：只按「单数 ≥ 2」分组，本数据会 100% 复购（客户全在老客户池里）；
  真实分析用「90/180 天内二次下单」。
- **`strftime`/`substr` 取月份**：日期列已统一为 `YYYY-MM-DD`，两者结果一致；
  `ORDER BY 1` 保证月份按字典序=时间序。

## 完整命令清单

```bash
# 准备
dtool convert --input sales.xlsx --sheet 订单 --name orders
dtool convert --input sales.xlsx --sheet 客户 --name customers

# 区域
dtool query --sql "SELECT 区域, COUNT(*) 单数, ROUND(SUM(金额)) 销售额 FROM orders WHERE 状态 <> '已退款' GROUP BY 1 ORDER BY 3 DESC"
# 月度
dtool query --sql "SELECT substr(日期,1,7) 月份, ROUND(SUM(金额)) 销售额 FROM orders WHERE 状态 <> '已退款' GROUP BY 1 ORDER BY 1"
# 区域 × 月
dtool query --sql "SELECT 区域, substr(日期,1,7) 月份, ROUND(SUM(金额)) 销售额 FROM orders WHERE 状态 <> '已退款' GROUP BY 1,2 ORDER BY 1,2"
# 客户 Top5 + 集中度
dtool query --sql "SELECT c.客户名称, COUNT(*) 单数, ROUND(SUM(o.金额)) 销售额 FROM orders o JOIN customers c ON o.客户ID=c.客户ID WHERE o.状态 <> '已退款' GROUP BY 1 ORDER BY 3 DESC LIMIT 5"
# 客单价
dtool query --sql "SELECT 区域, ROUND(AVG(金额)) 客单价 FROM orders WHERE 状态 <> '已退款' GROUP BY 1 ORDER BY 2 DESC"
```
