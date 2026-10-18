# 示例 05：多系列图表（长表透视 / 宽表多指标）

**场景**：同一份销售明细，业务方要看两种对比：

1. 「各区域 × 各月份」的销售额对比 —— 查询结果是长表（每行一个区域、一个月份）；
2. 「已完成 / 配送中」两条月度金额曲线 —— 查询结果本身就是宽表（每行多个指标列）。

**对 AI 说**：「用 sales.xlsx 出两张图：区域 × 月份的分组柱状图，以及已完成/配送中两条月度折线。」

**什么时候照这个示例做**

- 结果已经是 `GROUP BY x, group` 的长表，想直接出分组柱或多折线；
- 一条 SQL 已经算出多个指标列（宽表），想一次画成多系列；
- 想知道多系列参数怎么校验（互斥、同列、上限、重复组合）。

**数据源**：[`../data/sales.xlsx`](../data/sales.xlsx)，工作表 `订单`（120 行）。

## 它做了什么

| # | 命令 | 作用 |
|---|------|------|
| 1 | `dtool convert --input ../data/sales.xlsx --sheet 订单 --name sales` | 工作表 → 数据集 `sales`，后面两条查询共用 |
| 2 | `dtool query … GROUP BY "区域", "月份"` + `dtool visualize --type bar --x 区域 --y 销售额 --group-by 月份` | 长表透视：每个月份一个系列，出簇状柱状图 |
| 3 | 同一份长表 + `--type line --x 月份 --group-by 区域 --missing gap` | 缺失的 (区域, 月份) 直接断线，不把“没数据”画成 0 |
| 4 | `dtool query … 两个 SUM(CASE …) 指标列` + `dtool visualize --type line --x 月份 --y 已完成,配送中` | 宽表直取：逗号分隔的每个 `--y` 列一个系列，出多折线 |

## 运行

```bash
make build                                # 或 go build -o dtool .
bash examples/05-multi-series/run.sh      # 也可 DTOOL=/path/to/dtool bash …
```

`examples/run-all.sh` 会按目录名自动纳入本示例。

## 产物（`examples/out/05-multi-series/`）

| 文件 | 内容 |
|------|------|
| `grouped_bar.png` | 区域 × 月份的分组柱状图（每月一个系列） |
| `region_gap.png` | 区域 × 月份的多折线：缺失组合断线 |
| `multi_line.png` | 已完成 / 配送中两条月度折线 |
| `.dtool/` | 数据集 `sales`、两次查询与两次出图的 Action 记录 |

## 关键点

- **`--group-by` 与 `--y a,b` 互斥**：前者用于长表透视，后者用于宽表直取；同时给出会报参数错误（退出码 2）。
- **`--group-by` 不能与 `--x` / `--y` 同列**，否则会得到语义不明的图，直接报错。
- **顺序**：x 分类与系列都按输入顺序（SQL `ORDER BY` 说了算）；缺失组合默认补 0，`--missing gap` 时改为断线（仅 line）；重复组合会报错并提示先在 SQL 里聚合。
- **`--type pie` 只支持单系列**，带 `--group-by` 或多 `--y` 会提示改用 bar / line。
- **`--type table` 忽略多系列参数**，仍按原始行导出。
- Action 的 `input.y` 记录为**数组**（如 `["已完成","配送中"]`），`actions show` 可回溯这次画了哪些列。

## 接着看

- 同一场景的职业化写法：[docs/scenarios/sales.md](../../docs/scenarios/sales.md)
- 参数速查：[skills/dtool/SKILL.md](../../skills/dtool/SKILL.md)
- 其余示例：[examples/README.md](../README.md)
