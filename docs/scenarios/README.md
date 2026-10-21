# 场景手册：按角色上手 dtool

每个角色或业务场景一份文档，难度从 ★ 到 ★★★ 递进：从「一条 SQL 拿到答案」走到
「连表 + 出图 + 导出 + 留痕」。所有命令都能对着仓库里的示例数据直接跑。

> 这里原来按职业拆了 8 份，其中「销售 / 电商运营 / 市场增长」用的是同一份 `sales.xlsx`、
> 同一套口径，拆开只会各说一套（比如销售额含不含退款）；「管理者 / 汇报」讲的就是
> `pipeline` 一条龙，已并为一份。现在 5 份：销售（含电商/增长）、财务、HR、设计工作室经营、数据/BI。

## 准备（只做一次）

```bash
# 1. 构建（或从 Release 页面下载对应平台的二进制）
cd /path/to/dtool && make build

# 2. 建一个练习目录，把示例数据拷进去
mkdir -p /tmp/demo && cd /tmp/demo
cp /path/to/dtool/examples/data/*.xlsx .

# 3. 把工作表变成数据集（之后可反复查询，不必重复转换）
dtool convert --input sales.xlsx --sheet 订单   --name orders
dtool convert --input sales.xlsx --sheet 客户   --name customers
dtool convert --input hr.xlsx    --sheet 员工   --name hr
dtool convert --input messy.xlsx --sheet 原始数据 --name messy
# 工作室经营分析场景（四张表在同一个工作簿里）
for s in 员工 项目 报销 考勤; do
  dtool convert --input studio.xlsx --sheet "$s" --name "$s"
done
dtool datasets list
```

下文的 `dtool` 都指这个二进制，`.dtool/` 是它在当前目录下建的工作区。字段含义见
[examples/README.md](../../examples/README.md)。

## 场景一览

「对 AI 说」一列是可直接粘贴给 Agent（Copilot 等）的提示词，把文件名换成你自己的数据即可：

| 场景 | 典型问题 | 对 AI 说（示例） | 难度跨度 |
|------|----------|------------------|----------|
| [销售 / 电商 / 增长](sales.md) | 各区卖了多少？哪个 SKU 在挣钱？增长从哪来？ | 按区域汇总 `sales.xlsx` 的销售额并出柱状图，再看 SKU 销量与金额、月度环比与新客节奏 | 汇总 → JOIN → 占比 → 趋势图 → SKU/品类 → 环比（同一份数据，三份文档已合并） |
| [财务 / 会计](finance.md) | 收入口径对不对？有没有对不上的账？ | 核对 `sales.xlsx` 的金额是否等于单价 × 数量，列出退款明细导出 CSV，再出月度净收入对账表 | 分状态汇总 → 账实核对 → 交叉核对 |
| [HR / 人事](hr.md) | 各部门薪酬结构？谁是待评估新人？ | 汇总 `hr.xlsx` 各部门人数与平均月薪出饼图，并导出绩效为空的人员名单 | 分组统计 → 空值筛查 → 交叉表 → 窗口排名 |
| [设计工作室经营分析](studio.md) | 5 人工作室：谁在赚钱、哪单在亏、钱花在哪、人累不累？ | 用 `studio.xlsx` 的员工/项目/报销/考勤四张表算项目毛利、亏损项目、负责人产值与考勤加班，最后出一条龙趋势图 | 多表 JOIN → 成本口径 → 亏损定位 → 绩效排序 → 交叉验证（全程 DuckDB） |
| [数据 / BI](analyst.md) | 手上是 JSON、大文件、脏数据怎么查？ | 把这个大 JSON 用省内存的方式读进来查某列分布并导出 CSV；若是脏 Excel，先告诉我自动改了哪些 | 多数据源 → 内存控制 → 沙箱 → 多格式投递 |

## 不写 SQL 也能出周报

原来还有一份「管理者 / 汇报」场景，内容其实就是 `pipeline` 一条龙 + 留痕——
现在它散落在每份文档的 ★★★ 小节里，这里给个最小版本：

```bash
# 一条命令：Excel → 查询 → 图（各步骤可选，产物落盘，stdout 只给清单）
dtool pipeline --excel sales.xlsx --sheet 订单 \
  --sql "SELECT substr(日期,1,7) AS 月份, ROUND(SUM(金额)) AS 销售额
         FROM data WHERE 状态 <> '已退款' GROUP BY 1 ORDER BY 1" \
  --chart line --x 月份 --y 销售额 --title "月度销售额" \
  --output out/月度.png --tags 周报 --notes "口径：排除已退款"

# 事后追溯「这个数怎么来的」
dtool actions list --limit 3
dtool actions trace <action_id>
```

复用已持久化的数据（不重读 Excel）：`--input dataset:orders`；
把查询结果导成表格：`dtool query --format xlsx --output out/明细.xlsx --sql "..."`。

## 让 AI 帮你做（推荐用法）

dtool 面向 Agent 设计：你只描述**目标和产物**，不必指定命令，Agent 会自己决定
`convert → query → visualize → 导出` 的步骤，并把每步留成 Action。

- **说清三件事**：数据文件（或数据集名）、要什么产物（图 / 表 / Excel / CSV）、口径（时间段、
  是否含退款、Top N）。
- **别自己拼列名**：让 Agent 先 `dtool datasets show <name>` 看列名与类型，再写 SQL（列名写错会报 `no such column`）。
- **要结论不要过程**：默认强隐私，只把产物文件（`artifacts[].abs_path`）发给用户；需要具体值时，等用户本人把配置改成 `privacy: normal` 后，再用 `query --rows` 或 `dtool datasets values`。Agent 不得自行改配置绕过。
- **口径要落盘**：让 Agent 用 `--tags 月报 --notes "口径：含已退款"` 记录，之后 `actions list` / `trace` 能追溯。
- **出错就让它自查**：`dtool actions list --status failed --limit 1` → `actions show <id>`，错误里带
  `detail` / `hint`（例如「改用 `--load-mode stream`」「表名请用 `--source` 绑定」）。
  看 `error.code` 决定下一步：`5` = 被中断（Ctrl+C / 进程被杀）→ 直接重跑；`4` = 执行失败 → 按 hint 改输入。
- **反复迭代的写法**：「刚才那张图按季度再拆一下」→ Agent 会用 `--from action:<id>` 记录血缘，
  数据源改用 `dataset:` 而不重跑转换。

一次完整对话大概长这样（你只说目标，Agent 负责命令）：

```text
你  ｜ 用 sales.xlsx 的订单表按区域汇总销售额，出个柱状图。
AI  ｜ 已转换数据集 orders → 确认列名（区域/金额）→ 查询 → 出图
     ｜ 产物：region.png（Action 01M4…）
你  ｜ 再接上客户档案，给我金额 Top 5 客户的 Excel。
AI  ｜ JOIN customers → 导出 top_customers.xlsx（Action 01M4…）
你  ｜ 这张图的数字怎么来的？
AI  ｜ convert sales.xlsx → query（SQL 见下）→ visualize，口径「不含已退款」
```

Agent 侧的行为约定（先 `datasets show` 再写 SQL、默认只交付文件、失败先自查 Action）见
[SKILL.md](../../skills/dtool/SKILL.md)。

## 四条通用规则

1. **中文列名可以直接写**：`SELECT 区域, SUM(金额) FROM orders GROUP BY 1`（SQLite 与 DuckDB 都支持）。
   列名含空格、以数字开头或重名时用双引号，如 `"名称_2"`；**文件路径**当表名时必须双引号。
2. **SQL 常量用单引号**，所以整条 SQL 建议在 shell 里用双引号包住：
   `dtool query --sql "SELECT ... WHERE 状态 = '已退款'"`。
3. **`--max-rows` 超限会报错**（默认 10000），不会静默截断：加 `LIMIT`，或 `--max-rows 0` 放开。
4. **每条命令都会留痕**：`.dtool/actions/` 记录 SQL、参数与产物，`dtool actions list` 可回看。

难度标记：★ 一条命令 ｜ ★★ 需要连表或换输出格式 ｜ ★★★ 多步组合 + 导出 + 留痕。

更细的 SQL 规则与参数速查见 [SKILL.md](../../skills/dtool/SKILL.md)；内存与性能实测见
[docs/PERFORMANCE.md](../PERFORMANCE.md)。
