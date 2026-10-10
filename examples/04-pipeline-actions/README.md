# 示例 04：pipeline 一条龙 + Action 协作（复用、注释、血缘、失败诊断）

**场景**：你不想一条条敲命令 —— 给一个 Excel，直接要图要表；而且事后有人问「这个数字是怎么来的」，
你要能答出完整链路。

**对 AI 说**：「用 pipeline 把 sales.xlsx 一条龙出成月度折线图并标好口径，之后我要能追溯数字来源。」

**什么时候照这个示例做**

- 从原始 Excel 到一张图，希望一条命令搞定（转换 → 查询 → 出图）；
- 数据要留着反复查，不想每次都重新转换；
- 要给某次分析标注口径、追溯它派生了哪些结果；
- 命令失败时希望留下记录，并且能看到可操作的错误原因。

**数据源**：[`../data/sales.xlsx`](../data/sales.xlsx)，工作表 `订单`（120 行）。

## 它做了什么

| # | 命令 | 作用 |
|---|------|------|
| 1 | `dtool pipeline --excel … --sheet 订单 --name orders --sql … --chart line --output trend.png` | 一条命令完成 转换 → 查询 → 折线图；SQL 里用 `data` 指代刚转换的数据 |
| 2 | `dtool pipeline --input dataset:orders --sql …` | 数据已持久化，之后只查询不再转换 |
| 3 | `dtool actions annotate <查询id> --text "口径：…" --by user` | 把口径说明写进 Action，之后任何读取者都能看到 |
| 4 | `dtool query --from action:<id> …` + `dtool actions trace <id>` | `--from` 只记录血缘；`trace` 展示上游数据 → 本次查询 → 派生结果 |
| 5 | `dtool query --sql 'SELECT 不存在的列 FROM orders'` | 故意写错：失败也会被记录，`actions list --status failed` 能捞出来 |
| 6 | `dtool actions list --limit 8` | 最近做了什么、各自产出什么 |

## 运行

```bash
make build                                # 或 go build -o dtool .
bash examples/04-pipeline-actions/run.sh  # 也可 DTOOL=/path/to/dtool bash …
```

## 产物（`examples/out/04-pipeline-actions/`）

| 文件 | 内容 |
|------|------|
| `trend.png` | 月销售额趋势折线图 |
| `.dtool/datasets/orders/` | pipeline 持久化下来的数据集 |
| `.dtool/actions/` | 每个命令一条 Action：类型、状态、摘要、父/派生关系 |

`actions list` 里的记录长这样（摘要即结论，不必打开产物）：

```json
{"type":"convert","status":"success","summary":"转换 sales.xlsx → 数据集 orders（120 条记录）"}
{"type":"query","status":"success","summary":"查询返回 6 行"}
{"type":"visualize","status":"success","summary":"生成 line 图表：6 个数据点"}
```

失败也留痕，且能一眼看到原因：

```json
{"id":"01M4GHFW…","type":"query","status":"failed",
 "summary":"SQL logic error: no such column: 不存在的列 (1)"}
```

## 关键点

- **pipeline 内部 SQL 的表名固定是 `data`**，不是 `--name` 指定的数据集名。
- **步骤可裁剪**：不带 `--sql` 就只转换（把 Excel 变成可复用的数据集），不带 `--chart` 就只查询。
- **`--input`** 支持 `文件` / `dataset:<名>` / `action:<id>` / `latest:convert`。
- **Action 记录 = 审计线索**：`id` / `status` / `summary` / `parent_id` / `derived_from`，
  `trace` 回答「这张图是怎么来的」，`export` 可整包归档。

## 接着看

- 同一场景的职业化写法：[docs/scenarios/management.md](../../docs/scenarios/management.md)
- 连表与 SQL 规则：[skills/dtool/SKILL.md](../../skills/dtool/SKILL.md)
- 其余示例：[examples/README.md](../README.md)
