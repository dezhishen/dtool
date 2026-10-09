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

| 场景 | 典型问题 | 难度跨度 |
|------|----------|----------|
| [销售 / 业务运营](sales.md) | 各区卖了多少？哪些客户在贡献增长？ | 汇总 → JOIN → 占比 → 趋势图 |
| [财务 / 会计](finance.md) | 收入口径对不对？有没有对不上的账？ | 分状态汇总 → 账实核对 → 交叉核对 |
| [HR / 人事](hr.md) | 各部门薪酬结构？谁是待评估新人？ | 分组统计 → 空值筛查 → 交叉表 → 窗口排名 |
| [电商 / 仓储运营](ecommerce.md) | 哪些 SKU 是爆款？退款率、复购如何？ | SKU 排行 → 透视 → 复购结构 → 分区排名 |
| [市场 / 增长](marketing.md) | 增长从哪来？环比、新客、客单价？ | 趋势图 → 分布 → 环比 → 新客节奏 |
| [管理者 / 汇报](management.md) | 5 分钟给出一页图表，还要说得清来源 | 一条龙 → 复用 → 导出 → 留痕 |
| [数据 / BI](analyst.md) | 手上是 JSON、大文件、脏数据怎么查？ | 多数据源 → 内存控制 → 沙箱 → 多格式投递 |

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
