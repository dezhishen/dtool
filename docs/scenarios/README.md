# 场景手册：按职业上手 dtool

每个场景一份文档，难度从 ★ 到 ★★★ 递进：从「一条 SQL 拿到答案」走到「连表 + 出图 + 导出 + 留痕」。
所有命令都能对着仓库里的示例数据直接跑。

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
dtool datasets list
```

下文的 `dtool` 都指这个二进制，`.dtool/` 是它在当前目录下建的工作区。字段含义见
[examples/README.md](../../examples/README.md)。

## 场景一览

「对 AI 说」一列是可直接粘贴给 Agent（Copilot 等）的提示词，把文件名换成你自己的数据即可：

| 场景 | 典型问题 | 对 AI 说（示例） | 难度跨度 |
|------|----------|------------------|----------|
| [销售 / 业务运营](sales.md) | 各区卖了多少？哪些客户在贡献增长？ | 按区域汇总 `sales.xlsx` 的订单销售额并出柱状图，再给我金额 Top 5 客户的 Excel | 汇总 → JOIN → 占比 → 趋势图 |
| [财务 / 会计](finance.md) | 收入口径对不对？有没有对不上的账？ | 核对 `sales.xlsx` 的金额是否等于单价 × 数量，列出退款明细导出 CSV，再出月度净收入对账表 | 分状态汇总 → 账实核对 → 交叉核对 |
| [HR / 人事](hr.md) | 各部门薪酬结构？谁是待评估新人？ | 汇总 `hr.xlsx` 各部门人数与平均月薪出饼图，并导出绩效为空的人员名单 | 分组统计 → 空值筛查 → 交叉表 → 窗口排名 |
| [电商 / 仓储运营](ecommerce.md) | 哪些 SKU 是爆款？退款率、复购如何？ | 统计 `sales.xlsx` 各产品销量与退款率，再找出各区域销量前二的品类 | SKU 排行 → 透视 → 复购结构 → 分区排名 |
| [市场 / 增长](marketing.md) | 增长从哪来？环比、新客、客单价？ | 看 `sales.xlsx` 的月度销售额趋势与环比，再统计新客首单月份和城市分布，各出一张图 | 趋势图 → 分布 → 环比 → 新客节奏 |
| [管理者 / 汇报](management.md) | 5 分钟给出一页图表，还要说得清来源 | 用 `pipeline` 把 `sales.xlsx` 一条命令出成月度折线图并标注口径，之后我要能追溯它是怎么算出来的 | 一条龙 → 复用 → 导出 → 留痕 |
| [数据 / BI](analyst.md) | 手上是 JSON、大文件、脏数据怎么查？ | 把这个 1.5GB 的 JSON 用省内存的方式读进来查某列分布并导出 CSV；若是脏 Excel，先告诉我自动改了哪些 | 多数据源 → 内存控制 → 沙箱 → 多格式投递 |

## 让 AI 帮你做（推荐用法）

dtool 面向 Agent 设计：你只描述**目标和产物**，不必指定命令，Agent 会自己决定
`convert → query → visualize → 导出` 的步骤，并把每步留成 Action。

- **说清三件事**：数据文件（或数据集名）、要什么产物（图 / 表 / Excel / CSV）、口径（时间段、
  是否含退款、Top N）。
- **别自己拼列名**：让 Agent 先 `dtool datasets show <name>` 看列名与类型，再写 SQL（列名写错会报 `no such column`）。
- **要结论不要过程**：默认读 Action 的 `preview`（前 20 行）就够了，需要全量时再 `dtool actions output <id>`。
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

Agent 侧的行为约定（先 `datasets show` 再写 SQL、默认只读 preview、失败先自查 Action）见
[skills.md](../../skills.md)。

## 四条通用规则

1. **中文列名可以直接写**：`SELECT 区域, SUM(金额) FROM orders GROUP BY 1`。
   列名含空格或重名时用双引号，如 `"名称_2"`。
2. **SQL 常量用单引号**，所以整条 SQL 建议在 shell 里用双引号包住：
   `dtool query --sql "SELECT ... WHERE 状态 = '已退款'"`。
3. **`--max-rows` 超限会报错**（默认 10000），不会静默截断：加 `LIMIT`，或 `--max-rows 0` 放开。
4. **每条命令都会留痕**：`.dtool/actions/` 记录 SQL、参数与产物，`dtool actions list` 可回看。

难度标记：★ 一条命令 ｜ ★★ 需要连表或换输出格式 ｜ ★★★ 多步组合 + 导出 + 留痕。

更细的 SQL 规则与参数速查见 [skills.md](../../skills.md)；内存与性能实测见
[docs/PERFORMANCE.md](../PERFORMANCE.md)。
