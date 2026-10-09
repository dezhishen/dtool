# 示例 03：脏数据体检（空表头、重复表头、合并单元格、混合类型）

**场景**：同事「手工做的」Excel：表头有空列、有重名，`B2:B3` 是合并单元格，手机号带前导零，
金额列里混着 `N/A`，最后一行还少了一列。你要先知道工具**自动改了什么**，再用 SQL 把它洗干净。

**什么时候照这个示例做**

- 表格不规范，想先看「自动修正清单」（warnings）而不是自己一格格核对；
- 同一列数字和文本混排，`SUM` 之前必须显式转换；
- 需要判断某列到底有多少**有效值**，而不是有多少行；
- 想确认「脏」这件事有没有被静默处理掉。

**数据源**：[`../data/messy.xlsx`](../data/messy.xlsx)，工作表 `原始数据`，4 行数据（故意做脏：
第 2 列表头为空、`名称` 重复、第 4 行整行为空、`B2:B3` 合并、手机号前导零、`金额` 含 `N/A`、
末行缺 `数量`）。

## 它做了什么

| # | 命令 | 作用 |
|---|------|------|
| 1 | `dtool convert --input ../data/messy.xlsx --name messy` | 转换时把所有自动修正记录进 `warnings` |
| 2 | `dtool datasets show messy` | 看修正后的列名与类型（`金额` 因含 `N/A` 被判为 string） |
| 3 | `dtool query --sql "SELECT 编号, 手机号, CAST(NULLIF(金额, 'N/A') AS REAL) AS 金额 FROM messy"` | `N/A` → NULL，再转成数值参与计算 |
| 4 | `dtool query --sql '…COUNT("名称_2"), COUNT("数量")…'` | 统计每列的有效值个数（NULL 不计入） |

## 运行

```bash
make build                          # 或 go build -o dtool .
bash examples/03-messy-data/run.sh  # 也可 DTOOL=/path/to/dtool bash …
```

## 产物（`examples/out/03-messy-data/`）

| 文件 | 内容 |
|------|------|
| `.dtool/datasets/messy/` | 清洗后的数据集与 Schema |
| `.dtool/` 下 Action 记录 | `convert` 的 `warnings`、查询 SQL 与结果预览 |

控制台会直接打印 warnings（这就是「改了什么」的清单）：

```json
"warnings": [
  "检测到 1 个合并单元格，仅左上角有值，其余为 null",
  "第 2 列表头为空，已命名为 col_2",
  "表头 \"名称\" 重复，已重命名为 名称_2"
]
```

有效值统计：

```json
{"行数":4,"有规格":3,"有数量":3}
```

处理后金额为数值、手机号保留前导零：

```json
{"编号":1004,"手机号":"013700137000","金额":5999}
```

## 关键点

- **warnings 是「契约」**：所有自动修正都会列出（空表头命名、重名字段加后缀、合并单元格提示），
  不会静默改数据；读数据前先读它。
- **合并单元格只有左上角有值**，其余是 `null` —— 需要时用 `COALESCE`/`LAG` 补全。
- **`NULLIF(x,'N/A')` 优于直接 `CAST`**：后者会把 `N/A` 变成 `0`，悄悄污染求和。
- **`COUNT(列)` 只数非 NULL**，用来判断「这一列有多少有效值」。

## 接着看

- 同一场景的职业化写法：[docs/scenarios/analyst.md](../../docs/scenarios/analyst.md)
- SQL 规则与参数速查：[skills.md](../../skills.md)
- 其余示例：[examples/README.md](../README.md)
