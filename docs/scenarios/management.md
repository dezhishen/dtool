# 场景：管理者 / 汇报（PM）

> 你不打算写 SQL，也不想装一堆工具：给我一张图、一张表，还要说得清数字从哪来。
> 目标：一条命令出图 → 复用数据 → 导出投递 → 全链路留痕。

**对 AI 说**：「用 pipeline 把 sales.xlsx 一条命令出成月度折线图并标注口径，之后我要能追溯这个数字是怎么算出来的。」

## 准备

```bash
mkdir -p /tmp/demo && cd /tmp/demo
cp /path/to/dtool/examples/data/sales.xlsx .
```

## ★ 一条命令：Excel → 图

```bash
dtool pipeline --excel sales.xlsx --sheet 订单 --name orders \
  --sql "SELECT 区域, ROUND(SUM(金额),2) AS 销售额 FROM data GROUP BY 1 ORDER BY 销售额 DESC" \
  --chart bar --x 区域 --y 销售额 --title "各区域销售额" --output 区域销售额.png
```

一条命令内部完成了「转换 → 查询 → 出图」三步，并把数据集 `orders` 持久化下来。

- pipeline 里 SQL 的表名固定是 **`data`**（指本次转换出来的数据），不是数据集名。
- `--chart` 留空就是只转换不出图；`--chart table` 出表格。

## ★ 先把数据存好，之后反复用

```bash
dtool pipeline --excel sales.xlsx --sheet 订单 --name orders   # 不带 --sql = 只转换
dtool datasets list
dtool datasets show orders
```

数据集存在 `.dtool/datasets/<名字>/`，是一个普通 JSON 文件，可以直接发给同事或提交到仓库。

## ★★ 复用已持久化的数据出图

```bash
dtool pipeline --input dataset:orders \
  --sql "SELECT strftime('%Y-%m', 日期) AS 月份, ROUND(SUM(金额),2) AS 销售额 FROM data GROUP BY 1 ORDER BY 1" \
  --chart line --x 月份 --y 销售额 --title "月度销售额" --output 月度趋势.png
```

`--input` 支持 `文件` / `dataset:<名>` / `action:<id>` / `latest:convert`；内部 SQL 同样用 `data`。

## ★★ 给结论配一张表（Markdown / Excel）

```bash
dtool query --format markdown --sql "SELECT 状态, COUNT(*) AS 单数, ROUND(SUM(金额),2) AS 金额
  FROM orders GROUP BY 1"

dtool query --format xlsx --output 状态汇总.xlsx --sql "SELECT 状态, COUNT(*) AS 单数,
    ROUND(SUM(金额),2) AS 金额 FROM orders GROUP BY 1"
```

也可以把「刚查出来的那张表」直接另存成表格文件或 Markdown：

```bash
dtool visualize --input latest:query --type table --format xlsx --output 状态汇总.xlsx
dtool visualize --input latest:query --type table --format md   --output 状态汇总.md
```

## ★★★ 追问「这张图的数字是怎么来的？」

```bash
dtool query --tags 月报,汇报 --notes "口径：含已退款" --sql "SELECT 区域,
    ROUND(SUM(金额),2) AS 销售额 FROM orders GROUP BY 1"

dtool actions list --limit 5          # 最近做了什么，各自产出什么
dtool actions show <id>               # 完整参数、SQL、产物、结果预览
dtool actions annotate <id> --text "用于 10 月经营会"
dtool actions trace <id>              # 上游数据 -> 本次查询 -> 派生的图表
dtool actions export --limit 20 --output 月报留痕.json
```

`trace` 会把「Excel → 数据集 → 查询 → 图表」串成一条链，直接回答「这个数字是怎么算出来的」；
`export` 导出的 JSON 可以随月报一起归档。

## 常见坑

| 现象 | 处理 |
|------|------|
| pipeline 里 `FROM orders` 报错 | pipeline 的 SQL 表名是 `data` |
| 忘记带 `--sheet` 拿错工作表 | 先 `dtool datasets show`/Excel 里确认工作表名，或加 `--sheet 订单` |
| 图表中文变方框 | `--font /path/to/字体.ttf`，或在配置文件里设 `font:` |
| 每次都要重跑转换 | 先用不带 `--sql` 的 pipeline 转一次，之后用 `--input dataset:<名>` |

相关：[场景总览](README.md) · [skills.md](../../skills.md)
