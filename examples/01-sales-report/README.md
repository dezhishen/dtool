# 示例 01：销售报表（多工作表 → JOIN → 图表 → 导出）

**场景**：你收到一份销售工作簿 —— `订单` 工作表是逐单明细，`客户` 工作表是客户档案。
业务方要一张「各区域销售额」的图，外加一份「金额 Top 5 客户」的 Excel。

**对 AI 说**：「用 sales.xlsx 的订单表按区域汇总销售额并出柱状图，再给我金额 Top 5 客户的 Excel。」

**什么时候照这个示例做**

- Excel 里有多个工作表，希望拆成几张能反复查询的表，而不是每次重新读文件；
- 要跨表 JOIN（订单 ↔ 客户），又不想写代码导数据；
- 结果要同时给「开会看图的人」和「要 Excel 的人」；
- 想知道每一列被推断成了什么类型（数值 / 文本 / 日期 / 枚举值）。

**数据源**：[`../data/sales.xlsx`](../data/sales.xlsx)，`订单` 120 行 + `客户` 15 行，字段说明见
[示例总览](../README.md#数据字典)。

## 它做了什么

| # | 命令 | 作用 |
|---|------|------|
| 1 | `dtool convert --input ../data/sales.xlsx --sheet 订单 --name orders` | 工作表 → 数据集 `orders`（JSON + Schema：类型 / 可空 / 枚举） |
| 2 | `dtool convert … --sheet 客户 --name customers` | 同一个工作簿的另一个工作表 → 数据集 `customers` |
| 3 | `dtool datasets list` / `dtool datasets show orders --preview-rows 3` | 看有哪些数据集、列名叫什么、类型推断结果 |
| 4 | `dtool query --format markdown --output region.md --sql '…'` | 剔除已退款订单，按区域汇总，直接产出 Markdown 表格 |
| 5 | `dtool visualize --input latest:query --type bar …` | 复用上一步的结果出柱状图（`latest:query` = 最近一次查询，不重跑 SQL） |
| 6 | `dtool query --format xlsx --output top_customers.xlsx --sql '…JOIN…'` | `orders` JOIN `customers`，金额 Top 5 导出 Excel |

## 运行

```bash
make build                            # 或 go build -o dtool .
bash examples/01-sales-report/run.sh  # 也可 DTOOL=/path/to/dtool bash …
```

## 产物（`examples/out/01-sales-report/`）

| 文件 | 内容 |
|------|------|
| `region.md` | 各区域订单数与金额（已剔除「已退款」） |
| `region.png` | 上表的柱状图 |
| `top_customers.xlsx` | 金额 Top 5 客户及其等级 |
| `.dtool/` | 数据集 `orders` / `customers`、查询结果、全部 Action 记录 |

`region.md` 长这样：

```
| 区域 | 订单数 | 总金额 |
| --- | --- | --- |
| 西南 | 25 | 183414.6 |
| 华东 | 36 | 137816.1 |
```

## 关键点

- **一个工作表 = 一个数据集 = 一张表**：多工作表要分别 `convert`，之后反复查询不用再读 Excel。
- **数据集名就是表名**，不必写文件路径；含中文的列名用双引号 `"金额"`。
- **`latest:query`** 让「查询 → 出图」不必重跑 SQL；中间若插了别的命令，改用 `--input action:<id>`。
- **`--format`** 决定投递形态：`markdown` / `csv` / `xlsx` / `table` / `json`。

## 接着看

- 同一场景的职业化写法：[docs/scenarios/sales.md](../../docs/scenarios/sales.md)
- SQL 规则与参数速查：[skills.md](../../skills.md)
- 其余示例：[examples/README.md](../README.md)
