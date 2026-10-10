# 示例 02：人员分析（前导零工号、可空列、饼图、日期过滤、表格导出）

**场景**：HR 给你一份员工花名册，你要看各部门人力与薪酬结构、挑出还没打绩效的人，并把名单导给主管。

**对 AI 说**：「用 hr.xlsx 汇总各部门人数与平均月薪出饼图，并导出绩效为空的人员名单。」

**什么时候照这个示例做**

- 表里有 `00101` 这类前导零编号，担心被 Excel / 数据库当数字吃掉；
- 列里有空值（新人还没绩效），需要正确筛出来，而不是被算成 0；
- 日期是 `YYYY-MM-DD` 文本，要按时间区间过滤；
- 要出饼图，或把查询结果整表导成 Excel。

**数据源**：[`../data/hr.xlsx`](../data/hr.xlsx)，工作表 `员工`，60 行；工号从 `00101` 起，绩效在
2026 年入职者上为空。

## 它做了什么

| # | 命令 | 作用 |
|---|------|------|
| 1 | `dtool convert --input ../data/hr.xlsx --name hr` | 单工作表工作簿可直接转换（省略 `--sheet`） |
| 2 | `dtool datasets show hr --preview-rows 3` | 观察 Schema：`工号` 是 string（前导零不丢）、`绩效` 可空、`部门` 带枚举 |
| 3 | `dtool query --sql '…COUNT/AVG…GROUP BY 部门'` + `visualize --type pie` | 各部门人数与平均月薪 → 饼图 |
| 4 | `dtool query --format csv --output recent_hires.csv --sql '…WHERE 入职日期 >= …'` | 日期是 ISO 文本，可直接比较；导出 CSV |
| 5 | `dtool query --sql '…WHERE 绩效 IS NULL'` + `visualize --type table --format xlsx` | 用 `IS NULL` 找待评估人员，并整表导出 Excel |

## 运行

```bash
make build                            # 或 go build -o dtool .
bash examples/02-hr-analysis/run.sh   # 也可 DTOOL=/path/to/dtool bash …
```

## 产物（`examples/out/02-hr-analysis/`）

| 文件 | 内容 |
|------|------|
| `headcount.png` | 各部门人数占比（饼图） |
| `recent_hires.csv` | 2025-01-01 之后入职的员工，按入职日期倒序 |
| `no_rating.xlsx` | 绩效为空的员工名单（Excel 表格） |
| `.dtool/` | 数据集 `hr`、查询结果、Action 记录 |

部门统计长这样：

```json
{"部门":"销售部","人数":15,"平均月薪":24467}
{"部门":"技术部","人数":14,"平均月薪":26721}
```

`recent_hires.csv` 开头：

```csv
工号,姓名,部门,入职日期
00136,杨勇,销售部,2026-09-21
00150,赵静,产品部,2026-05-20
```

## 关键点

- **前导零不丢**：整列被推断为文本，`00101` 不会变成 `101`；按字符串比较/筛选即可。
- **空值用 `IS NULL`**：`= NULL` 永远不成立 —— 这是最常见的坑。
- **日期是 ISO 文本**，`"入职日期" >= '2025-01-01'` 直接可用，无需解析成日期类型。
- **`visualize --type table`** 可以把「刚查出来的结果」原样导出 xlsx / markdown，不画图。

## 接着看

- 同一场景的职业化写法：[docs/scenarios/hr.md](../../docs/scenarios/hr.md)
- SQL 规则与参数速查：[skills/dtool/SKILL.md](../../skills/dtool/SKILL.md)
- 其余示例：[examples/README.md](../README.md)
