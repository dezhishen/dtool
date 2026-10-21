# 场景：数据 / BI

> 你接到的东西五花八门：系统导出的 JSON、几百 MB 的大文件、Excel 里的脏表。
> 你要把它们放进同一个 SQL 平面，并且保证三件事：**内存可控、边界清楚、结论可复现**。
>
> 这份文档和其他场景的区别是：别人问「卖了多少」，你问「这个数**敢不敢信**」——
> 数据被自动改过什么、内存够不够、危险 SQL 拦没拦、结果给了谁。

**对 AI 说**：「把这个 JSON 用省内存的方式读进来查某列分布并导出 CSV；若是脏 Excel，先告诉我工具自动改了哪些。」

## 准备

```bash
mkdir -p /tmp/demo && cd /tmp/demo
cp /path/to/dtool/examples/data/*.xlsx .
dtool convert --input sales.xlsx --sheet 订单 --name orders
dtool convert --input sales.xlsx --sheet 客户 --name customers
dtool convert --input messy.xlsx --sheet 原始数据 --name messy
```

## 先定口径

| 问题 | 本场景的答案 | 为什么重要 |
|------|-------------|-----------|
| 行数 = 记录数？ | `COUNT(*)` 是**行数**，`COUNT(某列)` 才是**有效值数** | 脏表里两者能差 20%，报表口径必须说清用哪个 |
| 内存够不够？ | 先看 `--load-mode auto` 选档结果（stderr/Action 的 `strategy`），别靠猜 | 猜错的代价是被内核 OOM 杀掉，而不是一条可读错误 |
| 脏数据改了什么？ | 只有 `convert` 的 `warnings` 算数：空表头/重复表头/合并单元格都在里面 | 「自动修正」如果不说，后续所有结论都建立在不透明的前提上 |
| 分析能不能复现？ | SQL + 参数 + `--notes` 一起留在 Action 里 | 三个月后有人问「这数怎么来的」，只有它能回答 |

## ★ 把任意 JSON 直接当表查

**对 AI 说**：「直接把这个 JSON 当表查一下行数」

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

**对 AI 说**：「这个文件用省内存的方式读，别把机器撑爆」

```bash
dtool --load-mode stream query --sql "SELECT COUNT(*) AS 行数 FROM orders"  # 流式，省内存
dtool --load-mode full   query --sql "SELECT COUNT(*) AS 行数 FROM orders"  # 整块解析，更快
dtool --load-mode auto   query --sql "SELECT COUNT(*) AS 行数 FROM orders"  # 默认：按大小自适应
```

装不下时的默认行为是**先警告、再按最省档试一次**（`--mem-policy try`），
而不是被内核 OOM 杀掉：

```bash
$ dtool --max-memory 32K query --sql "SELECT COUNT(*) AS 行数 FROM orders"
警告：所有执行档的预计峰值都超出预算（stream+disk 预计 24MB > 阈值 27KB），将按最省档 stream+disk 试一次；
若失败，请换 --store disk、拆分输入或调大 --max-memory。
{"success": true, "columns": ["行数"], "row_count": 1}
```

要「预算不够就立刻失败」用 `--mem-policy strict`：

```bash
$ dtool --max-memory 32K --mem-policy strict query --sql "SELECT COUNT(*) AS 行数 FROM orders"
{"error":"预计内存不足，已按 --mem-policy strict 中止：27KB",
 "detail":"最省档 stream+disk 预计需 34MB，阈值 27KB（--max-memory）；full+memory：预计 38MB > 上限 22KB…",
 "hint":"拆分或裁剪输入后重试；确需强制运行时用 --load-mode/--store 指定档位，或 --max-memory 0 关闭检查",
 "code":4}
```

**读法**：`try` 是「宁可慢也别崩」，`strict` 是「别浪费我时间」。批处理用 `strict`（失败即报警），
交互式分析用 `try`（等一会儿总比没有结果好）。

- 估算公式：JSON 整块 ≈ 文件 × 13、流式 ≈ 文件 × 2；Excel 转换 ≈ 固定 32MB + 文件 × 6。
- 预算来源：`--max-memory` > cgroup > 系统可用内存，且只按 85% 计算。
- `--load-mode` 只对 JSON 装入（`query`）有效，且对 JOIN 中**每个**数据源分别生效；`convert` 没有这个参数
  —— Excel 转换只有一个流式实现，要降内存只能缩小输入（拆文件、裁列）；传了不生效，CLI 会在 stderr 提示「已忽略」。
- 确需强行运行时 `--max-memory 0` 关闭检查；真的超预算时，看门狗会先在 stderr 打印「内存超出预算（已用 xx，预算 xx），正在中止」，再以 `code: 4` 失败，不会静默卡住。
- 预算的自动探测：Linux 用 cgroup/系统可用内存，Windows 用 Job Object 上限，**macOS 等平台没有**——
  在容器或受限环境里务必显式 `--max-memory`；拿不到预算时顶到硬上限可能只剩 SQLite `out of memory (7)` / DuckDB `Out of Memory Error`
  甚至 Go runtime 的 fatal error。
- 1 核 2GB 下的实测数字与能力边界见 [docs/PERFORMANCE.md](../PERFORMANCE.md)。

## ★★ 沙箱：危险 SQL 会被拦

**对 AI 说**：「试试写操作会不会被拦住」

```bash
dtool query --sql "SELECT 1; DROP TABLE orders"
# {"error":"sandbox violation: multiple statements are not allowed","code":4}

dtool query --sql "DELETE FROM orders"
# {"error":"sandbox violation: only SELECT/WITH statements are allowed","code":4}
```

只允许**单条 `SELECT`/`WITH`**，且只能读工作区/当前目录/`--source` 指定的文件。确需放开时用
`--sandbox=false`，风险自负。

## ★★ 脏数据体检

**对 AI 说**：「这个脏 Excel 自动改了哪些？金额里的 N/A 该怎么处理」

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

## ★★ 多系列对比（长表 / 宽表）

**对 AI 说**：「同一份结果既要按地区 × 月份看分组柱，也要把两个指标画成两条折线。」

```bash
# 长表：结果每行是 (地区, 月份, 值)
dtool visualize --input latest:query --type bar --x 地区 --y 销售额 --group-by 月份
dtool visualize --input latest:query --type line --x 月份 --y 销售额 --group-by 地区 --missing gap

# 宽表：结果本身就有多列指标
dtool visualize --input latest:query --type line --x 月份 --y 销售额,成本
```

- `--group-by` 与多 `--y` 互斥：前者做长表透视，后者做宽表直取。
- 缺失组合默认补 0；`--missing gap` 仅 `line` 支持，缺口处断线。
- 系列数上限 50；重复 `(x, group)` 会报错并提示先聚合。

## ★★★ 多格式投递

**对 AI 说**：「同一份结果分别给我 markdown 和 CSV」

```bash
dtool query --format markdown --sql "SELECT ..."                        # 贴 PR / 文档
dtool query --format table    --sql "SELECT ..."                        # 终端里看
dtool query --format csv      --output 结果.csv  --sql "SELECT ..."     # 给下游程序
dtool query --format xlsx     --output 结果.xlsx --sql "SELECT ..."     # 给业务
dtool visualize --input latest:query --type table --format md --output 结果.md   # 复用上次查询
```

想固定口径，把 SQL 与参数写进命令里并加 `--notes "口径说明"`，Action 记录会把两者一起留存，
配合 `dtool actions export` 归档即可复现任意一次分析。

## 一次典型体检的输出长什么样

同一份「脏 Excel + 大 JSON」跑完，你应当能给出这五句话：

1. **数据被改过什么**：3 项自动修正（合并单元格提示、空表头 → `col_2`、重复表头 → `名称_2`），
   全部在 `convert` 的 `warnings` 里，没有静默处理。
2. **类型靠不靠得住**：`金额` 列含 `N/A` → 整列判为 `string`（不是把它们变成 0），
   要用数值就先 `NULLIF`/`CASE` 显式转换。
3. **内存够不够**：`query` 的 `strategy` 说明用了哪档（`stream+disk` 说明降档了），
   `strategy_note` 说明原因；跑之前 `dtool meminfo` 可以预演。
4. **边界拦没拦**：多语句与写操作都返回 `sandbox violation`（code 4）；
   需要放开时是显式 `--sandbox=false`，不是悄悄绕过。
5. **能不能复现**：SQL、`--load-mode`、`--max-memory`、`--notes` 都在 Action 里，
   `actions export` 归档即可重放。

## 常见坑

| 现象 | 处理 |
|------|------|
| `--source x=data.xlsx` 报错 | `query` 只读 JSON；xlsx 先 `dtool convert` |
| `no such table: xxx` | 表名拼错：用 `--source` 绑定，或写对数据集名 / 双引号路径 |
| `result exceeds --max-rows` | 结果超 10000 行：加 `LIMIT`、调大 `--max-rows`，或 `--max-rows 0` |
| 预检拦得太早 | 估算按 ×2 / ×13 保守取值；确认内存够就 `--max-memory 0` |
| 单引号包了文件路径 | 单引号是**字符串**不是标识符，路径要用双引号 |
| 以为 `--max-memory` 会直接失败 | 默认 `--mem-policy try` 只警告并按最省档试跑；要硬失败加 `strict` |
| 把 `COUNT(*)` 当有效值 | 缺值列要用 `COUNT(列)`；`COUNT(*)` 数的是行 |

相关：[性能与边界](../PERFORMANCE.md) · [场景总览](README.md) · [SKILL.md](../../skills/dtool/SKILL.md)
