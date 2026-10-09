# 场景：数据 / BI

> 你接到的东西五花八门：系统导出的 JSON、几百 MB 的大文件、Excel 里的脏表。
> 目标：把它们放进同一个 SQL 平面，并且让内存可控、结果可投递、口径可复现。

## 准备

```bash
mkdir -p /tmp/demo && cd /tmp/demo
cp /path/to/dtool/examples/data/*.xlsx .
dtool convert --input sales.xlsx --sheet 订单 --name orders
dtool convert --input sales.xlsx --sheet 客户 --name customers
dtool convert --input messy.xlsx --sheet 原始数据 --name messy
```

## ★ 把任意 JSON 直接当表查

```bash
dtool query --source j=.dtool/datasets/orders/data.json \
  --sql "SELECT COUNT(*) AS 行数 FROM j"
```

表名有三种来源，按优先级解析：

1. `--source 别名=引用` —— 引用可以是文件路径、`dataset:<名>`、`action:<id>`、`latest:<type>`；
2. FROM 里的**数据集名**（如 `FROM orders`）；
3. **双引号包起来的 JSON 文件路径** `FROM "./x.json"`（沙箱开启时须在工作区/当前目录内）。

```bash
# 混合使用：一个数据集 + 一个裸文件
dtool query --source o=dataset:orders --source c=.dtool/datasets/customers/data.json \
  --sql "SELECT COUNT(*) AS 行数 FROM o JOIN c ON o.客户ID = c.客户ID"
```

注意：**`query` 直接吃 JSON，不吃 xlsx** —— Excel 先 `dtool convert` 成数据集（支持多工作表、
表头修正与类型推断），之后就当成普通表用。

## ★★ 内存可控

```bash
dtool --load-mode stream query --sql "SELECT COUNT(*) AS 行数 FROM orders"  # 流式，省内存
dtool --load-mode full   query --sql "SELECT COUNT(*) AS 行数 FROM orders"  # 整块解析，更快
dtool --load-mode auto   query --sql "SELECT COUNT(*) AS 行数 FROM orders"  # 默认：按大小自适应
```

装不下时会在**载入前**报错，而不是被内核 OOM 杀掉：

```bash
dtool --max-memory 32K query --sql "SELECT COUNT(*) AS 行数 FROM orders"
```

```json
{"error":"预计内存不足，已中止：数据源 27KB 预计需约 53KB","code":4,
 "detail":"含流式装入，按整块 13 倍 / 流式 2 倍估算需 53KB，当前可用约 27KB（--max-memory）",
 "hint":"改用流式解析（--load-mode stream）、拆分或裁剪输入后重试；确需强制运行时加 --max-memory 0 关闭该检查"}
```

- 估算公式：JSON 整块 ≈ 文件 × 13、流式 ≈ 文件 × 2；Excel 转换 ≈ 固定 32MB + 文件 × 6。
- 预算来源：`--max-memory` > cgroup > 系统可用内存，且只按 85% 计算。
- 确需强行运行时 `--max-memory 0` 关闭检查；`--load-mode` 对 JOIN 中**每个**数据源分别生效。
- 1 核 2GB 下的实测数字与能力边界见 [docs/PERFORMANCE.md](../PERFORMANCE.md)。

## ★★ 沙箱：危险 SQL 会被拦

```bash
dtool query --sql "SELECT 1; DROP TABLE orders"
# {"error":"sandbox violation: multiple statements are not allowed","code":4}

dtool query --sql "DELETE FROM orders"
# {"error":"sandbox violation: only SELECT/WITH statements are allowed","code":4}
```

只允许**单条 `SELECT`/`WITH`**，且只能读工作区/当前目录/`--source` 指定的文件。确需放开时用
`--sandbox=false`，风险自负。

## ★★ 脏数据体检

```bash
dtool convert --input messy.xlsx --sheet 原始数据 --name messy
```

转换结果里带 `warnings`，把「悄悄改了什么」讲清楚：

```json
"warnings": [
  "检测到 1 个合并单元格，仅左上角有值，其余为 null",
  "第 2 列表头为空，已命名为 col_2",
  "表头 \"名称\" 重复，已重命名为 名称_2"
]
```

再用 SQL 做清洗（前导零保留、`N/A` 转 NULL）：

```bash
dtool query --sql "SELECT 编号, 名称,
    CAST(手机号 AS TEXT) AS 手机号,
    CASE WHEN 金额 = 'N/A' THEN NULL ELSE CAST(金额 AS REAL) END AS 金额
  FROM messy"
```

```json
{"编号":1001,"名称":"机箱","手机号":"013800138000","金额":100}
{"编号":1003,"名称":"风扇","手机号":null,"金额":null}
```

## ★★★ 多格式投递

```bash
dtool query --format markdown --sql "SELECT ..."                        # 贴 PR / 文档
dtool query --format table    --sql "SELECT ..."                        # 终端里看
dtool query --format csv      --output 结果.csv  --sql "SELECT ..."     # 给下游程序
dtool query --format xlsx     --output 结果.xlsx --sql "SELECT ..."     # 给业务
dtool visualize --input latest:query --type table --format md --output 结果.md   # 复用上次查询
```

想固定口径，把 SQL 与参数写进命令里并加 `--notes "口径说明"`，Action 记录会把两者一起留存，
配合 `dtool actions export` 归档即可复现任意一次分析。

## 常见坑

| 现象 | 处理 |
|------|------|
| `--source x=data.xlsx` 报错 | `query` 只读 JSON；xlsx 先 `dtool convert` |
| `no such table: xxx` | 表名拼错：用 `--source` 绑定，或写对数据集名 / 双引号路径 |
| `result exceeds --max-rows` | 结果超 10000 行：加 `LIMIT`、调大 `--max-rows`，或 `--max-rows 0` |
| 预检拦得太早 | 估算按 ×2 / ×13 保守取值；确认内存够就 `--max-memory 0` |
| 单引号包了文件路径 | 单引号是**字符串**不是标识符，路径要用双引号 |

相关：[性能与边界](../PERFORMANCE.md) · [场景总览](README.md) · [skills.md](../../skills.md)
