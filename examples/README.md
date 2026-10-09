# 示例

四个可直接运行的示例，数据源在 [data/](data/)（由 `make examples-data` 用固定随机种子生成，可复现）。

```bash
make build                    # 先构建 dtool
bash examples/run-all.sh      # 运行全部；或 make examples
bash examples/01-sales-report/run.sh   # 单个示例
```

每个示例会把工作区（`.dtool/`）与图表、报表输出到 `examples/out/<示例名>/`（已被 `.gitignore` 忽略）。
`DTOOL=/path/to/dtool` 指定二进制；图表中文默认自动使用系统字体，也可 `FONT=/path/to/font.ttf` 指定。

| 示例 | 数据源 | 演示内容 |
|------|--------|----------|
| [01-sales-report](01-sales-report/run.sh) | `sales.xlsx`（订单 120 行、客户 15 行） | 多工作表导入为独立数据集、`datasets show` 看 Schema、数据集名当表名、两个数据集 JOIN、`latest:query` 复用结果出柱状图、导出 Markdown / Excel |
| [02-hr-analysis](02-hr-analysis/run.sh) | `hr.xlsx`（员工 60 行） | 前导零工号保持文本、可空列与枚举 Schema、饼图、按日期过滤导出 CSV、`IS NULL` 查询并导出 Excel 表格 |
| [03-messy-data](03-messy-data/run.sh) | `messy.xlsx`（故意的脏数据） | 空表头 / 重复表头 / 空行 / 合并单元格 / 混合类型的自动处理与 `warnings`，`CAST` + `NULLIF` 的处理办法 |
| [04-pipeline-actions](04-pipeline-actions/run.sh) | `sales.xlsx` | `pipeline` 一条龙出折线图、复用已持久化数据集、`annotate` 注释、`--from` 血缘与 `trace`、失败诊断 |

## 数据字典

**sales.xlsx**
- 工作表 `订单`：订单号、日期（`YYYY-MM-DD` 文本）、区域、客户ID、产品、数量、单价、金额、状态（已完成 / 配送中 / 已退款），覆盖 2026-01 至 2026-06。
- 工作表 `客户`：客户ID、客户名称、等级、城市、注册日期。

**hr.xlsx**，工作表 `员工`：工号（`00101` 起，文本）、姓名、部门、职级、月薪、入职日期、绩效（2026 年入职者为空）、邮箱。

**messy.xlsx**，工作表 `原始数据`：第 2 列表头为空、`名称` 重复、第 4 行整行为空、`B2:B3` 合并、手机号带前导零、`金额` 含 `N/A`、最后一行缺 `数量`。

## 重新生成数据源

```bash
make examples-data   # = go run ./examples/tools/genxlsx
```
