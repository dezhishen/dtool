---
name: dtool
description: 本地数据 Pipeline CLI（默认强隐私：结果只落盘，stdout 只给交付清单）。用于把 Excel 转成可复用的数据集（JSON + Schema），用 SQL 查询，输出表格（CSV/Markdown/Excel）或图表（PNG/SVG），并把每一步记录为可追溯的 Action。当用户要分析 Excel 数据、做汇总/图表/导出，或追问"之前的结果/进度"时使用。
---

# dtool 使用指南（Agent Skill 正文）

单二进制，内置 SQLite 与 DuckDB 两个查询引擎（`--engine` 切换，每个二进制都含两种）。
所有命令的 **stdout 都是结构化 JSON**；失败时 stdout 仍是错误 JSON、退出码非 0，人类可读信息写 stderr。

## 0. 最短路径

```bash
dtool convert --input sales.xlsx --name sales          # Excel → 数据集（JSON + Schema）
dtool datasets show sales                             # 结构：列名/类型/可空/基数/角色（不含任何值）
dtool query --sql 'SELECT region, SUM(amount) AS total FROM sales GROUP BY region ORDER BY total DESC'
dtool visualize --input latest:query --type bar --x region --y total --output out/sales.png
```

结构不确定时（标题行、多级表头、合计行、脏列）：

```bash
dtool convert --input 报表.xlsx --rehearse                    # 预演练：候选方案 + 打分 + 影响（只读）
dtool convert --input 报表.xlsx --inspect > layout.json       # 当前方案 + 待确认问题（只读）
# 改 layout.json（header_row、columns[].name/type，删掉 questions）后：
dtool convert --input 报表.xlsx --layout layout.json --name 报表
```

**默认强隐私**：结果只落盘，stdout 只给交付清单（`artifacts[]` + `send`）。
把 `artifacts[].abs_path` 作为文件附件发给用户，**不读取、不复述内容**。

## 1. 安装与升级

单二进制（Linux amd64/arm64、macOS amd64/arm64、Windows amd64）。从 Releases 下载对应平台的包：

| 平台 | 包名 |
|------|------|
| Linux / macOS | `dtool_<版本>_<os>_<arch>.tar.gz` |
| Windows | `dtool_<版本>_windows_<arch>.zip`（内含 `dtool.exe`） |

```bash
# 最新正式版：不需要手改版本号；预览版把 latest 换成 /releases 列表第一项，指定版本写 V=v1.2.3
V=$(curl -sSL https://api.github.com/repos/dezhishen/dtool/releases/latest \
  | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')
[ -n "$V" ] || { echo "取最新版本失败：手动指定 V=vX.Y.Z 后重试" >&2; exit 1; }
base=https://github.com/dezhishen/dtool/releases/download/$V
curl -LO "$base/dtool_${V#v}_linux_amd64.tar.gz" && curl -LO "$base/checksums.txt"
sha256sum -c --ignore-missing checksums.txt          # macOS 用 shasum -a 256 -c
tar -xzf "dtool_${V#v}_linux_amd64.tar.gz" && install -m 0755 dtool ~/.local/bin/
dtool version                                        # 确认可用（含 version/channel/os/arch）
```

- 装完包内还有 `README.md`、`SKILL.md`、`LICENSE`、`THIRD_PARTY_NOTICES.md`。`SKILL.md` 就是本文件
  （仓库里在 `skills/dtool/SKILL.md`），解包后可直接丢进平台技能目录，
  例如 `mkdir -p ~/.claude/skills/dtool && cp SKILL.md ~/.claude/skills/dtool/`。
- 升级自身：`dtool --update [--pre]` 只检查，`dtool upgrade [--pre]` 动手（下载后校验 `checksums.txt`
  的 sha256 并跑一次自检，任一步失败都不改动现有文件）。二进制要放在**有写权限**的目录；
  网络瞬断按指数退避自动重试 3 次，彻底失败时按提示去 Releases 手动下载。
- **同时也更新本文件**：`dtool upgrade --skills[=路径]`——目录写 `<目录>/SKILL.md`，明确 `.md` 结尾的写该文件，
  裸 `--skills` 写当前目录；装进平台技能目录就是 `dtool upgrade --skills=~/.claude/skills/dtool/SKILL.md`
  （目录不存在会创建）。手册取自发布归档（与二进制同一次 sha256 校验），不会出现「二进制旧、手册新」；
  已是最新版本时也能单独取（不碰二进制），`skills_changed` 说明内容有没有变。
  v0.2.0 及以前的归档里它叫 `skills.md`，两种名字都能取到，落点统一成 `SKILL.md`。
  反向不成立：v0.2.0 的旧二进制只按 `skills.md` 找，遇到 0.2.1+ 的归档会报「归档里没有 skills.md」
  且不替换二进制——先不带 `--skills` 升级一次即可。
- **只有预览 tag 时加 `--pre`**，否则预览版不算可升级版本；看 `dtool version` 的 `channel`
  （stable/preview/dev/local）确认当前构建类型。

## 2. 核心概念

| 概念 | 说明 |
|------|------|
| 工作区 | 默认 `./.dtool`（`--workspace` 或 `-c config.yaml` 指定），所有状态都在里面，可整体搬迁 |
| 数据集 | `convert` 的产物，位于 `.dtool/datasets/<name>/`：`data.json`、`data.schema.json`（必有，**只含结构**：列类型/可空/唯一/基数/角色/`updated_at`，不含枚举/样例/极值）、`dataset.json`。独立于 Action，可随时复用；同名重新转换会覆盖并刷新 `updated_at` |
| Action | 每次命令的完整记录（输入、产物、预览、错误、注释），位于 `.dtool/actions/`。读 Action 即可理解历史，无需重跑 |
| 引用 | `dataset:<name>`、`action:<id>`、`latest:<type>`（最近一次成功的 convert/query/…）、或文件路径 |

## 3. 隐私（默认 strong）

- 默认 `privacy: strong`。结果类命令的 stdout **只有交付清单**：`action_id` + `artifacts[]`
  （`path`/`abs_path`/`mime`/`bytes`/`sha256`）+ `send`（怎么发），不含 `columns`、`rows`、
  `row_count`、`preview`。
- **Agent 不得修改配置文件里的 `privacy`，不得新建/改写配置后用 `-c` 传入，也不得用参数绕过**。
  它只能由用户本人显式改；没有命令行开关、不读环境变量。`-c` 指向的配置应视为用户输入。
- 强隐私下以下动作直接失败（退出码 `6`）：`query --rows`、`pipeline --rows`、
  `query --format csv|markdown|table` 不带 `--output`、`datasets values`、`actions show --full`、`actions output`。
- 结构类命令仍可用：`datasets show` 只给 `type`/`nullable`/`unique`/`distinct_count`/`role`，
  不含 `enum`/`samples`/`min`/`max`；需要枚举值必须等用户改成 `normal` 后再 `datasets values`。
- `--inspect` / `--rehearse` 是**只读结构探测**，强隐私下允许：只输出表头行号、列名、类型、计数与候选，
  默认不带单元格内容（`normal` 才附带候选表头行的样例值）。不要用预演练绕过隐私。
- 结果集管理（`actions results list|stat|verify|gc`）只输出元数据或删除文件，strong 下可用；
  `gc` 默认 dry-run，只有 `--apply` 才删。
- dtool 不能阻止调用方直接读文件，**强隐私是契约，不是沙箱**：不要主动去读产物文件。

## 4. 标准流程

```bash
# 1. 导入：Excel → 数据集（Schema 自动生成）
dtool convert --input sales.xlsx --name sales
dtool datasets show sales              # 位置 + 结构（类型/可空/唯一/基数/角色；不含任何值）
dt delivery <action_id>                # 交付清单；或直接在刚才的 stdout 里取 artifacts

# 2. 查询：数据集名直接当表名
dtool query --sql 'SELECT region, SUM(amount) AS total FROM sales GROUP BY region ORDER BY total DESC'

# 2b. 结果集 → 独立数据集（数据只在本地流转；strong 下只给清单）
dtool datasets create monthly --result latest:query

# 3. 出图 / 导出（产物落盘，stdout 仍是交付清单）
dtool visualize --input latest:query --type bar --x region --y total --title "区域销量" --output out/sales.png
dtool visualize --input latest:query --type bar --x region --y total --group-by quarter  # 分组柱 / 多条折线
dtool visualize --input latest:query --type line --x month --y sales,profit              # 宽表多列 → 多折线
dtool query --sql '...' --format xlsx --output out/report.xlsx

# 一条龙（各步骤可选）
dtool pipeline --excel sales.xlsx --sql 'SELECT ... FROM data GROUP BY ...' --chart bar --x region --y total
dtool pipeline --excel sales.xlsx                                 # 只转换并持久化
dtool pipeline --input dataset:sales --sql 'SELECT ... FROM data'  # 复用已有数据
```

## 5. 命令速查

| 命令 | 要点 |
|------|------|
| `convert --input f.xlsx [--sheet S] [--name N] [--converter auto\|xlsx] [--inspect] [--rehearse] [--layout f.json]` | 仅 `.xlsx`；`--name` 缺省取文件名（指定 sheet 时追加 `_<sheet>`）。`--inspect`/`--rehearse` 只读、只出结构，可反复跑；`--layout` 按确认稿转换（不再自动探测）。**不接受** `--source`（那是 query 的参数）；`--engine`/`--load-mode` 会被忽略并提示 |
| `datasets list \| show <name> \| values <name> --column C \| create <name> --result R \| delete <name>` | `show` 只给结构（无 enum/samples/min/max）；`values` 是数据动作，仅 `normal` 可用（strong 下 code 6）；`create --result action:<id>\|latest:query` 把结果集提升为数据集（strong 下仍可用，只给清单） |
| `query --sql ... [--source 别名=引用]... [--format json\|csv\|markdown\|table\|xlsx] [--output f] [--rows] [--max-rows N] [--timeout 60s] [--load-mode auto\|stream\|full]` | 默认只返回交付清单；`--rows` 仅 `normal`；`xlsx` 必须配 `--output`；`--from <ref>` 只记血缘；**没有 `--json` 开关**（结果固定写 `result.json`） |
| `visualize --input <ref> --type bar\|line\|pie\|table --x X --y Y[,Y2...] [--group-by G] [--missing zero\|gap] [--format png\|svg] [--output f] [--font f.ttf]` | `table` 用 `--format md\|xlsx`，无需 x/y 且忽略多系列参数；y 必须是数值列；`--group-by`（长表）与多 `--y`（宽表）互斥、仅 bar/line、不能与 x/y 同列；缺失组合默认补 0，`--missing gap`（仅 line）改断线；重复 (x, group) 报错 |
| `pipeline --excel f \| --input ref [--sql ...] [--chart ...] [--rows]` | `--chart` 必须有 `--sql`；SQL 里用 `data` 指代上游数据；`--rows` 仅 `normal` |
| `actions list [--limit N --type T --status S]` | 最新在前；状态含 `stale`（进程已死的 running，被看护进程自动收敛，见 §13.5） |
| `actions results list\|stat\|verify\|gc` | 只处理 `output.files` 元数据：枚举/统计/校验哈希/回收（`gc` 默认 dry-run，`--apply` 才删；只动工作区 `outputs/`，外部输出、被引用结果、`--keep-last` 受保护） |
| `actions show <id> [--full] \| output <id> \| trace <id> \| annotate <id> --text ... --by ai-agent \| export \| reindex \| sync` | `show` 默认不输出 `output.preview`；`--full`/`output` 仅 `normal`（strong 下 code 6）；`export` 在 strong 下自动剥掉 preview；`annotate` 写用户口径/备注；`sync` 收敛状态 |
| `deliver <id>` | 只返回产物清单（`path`/`abs_path`/`mime`/`bytes`/`sha256` + `send`），不读内容；strong 下可用 |
| `meminfo [--source alias=文件]...` | 内存排查入口：预算来源（cgroup / Job Object / 系统可用内存 / `--max-memory`）、原始探测字段、看门狗开关，以及按当前输入的**预检预演**（`verdict: ok/borderline/risky/forced`、`plan.chosen` 与理由）。「预检为什么没拦住」先跑它 |
| `--store auto\|memory\|disk` | 查询库落在哪（两引擎通用）：auto 按预算选档 / memory 快 / disk 峰值最低 |
| `--mem-policy try\|strict` | 都预计超预算时：try 仍试最省档（失败记入 Action）/ strict 直接失败 |
| `--sandbox` | 默认开启：只允许单条 SELECT，且只能读工作区 / 当前目录 / `--source` 文件；`--sandbox=false` 关闭 |
| `-c, --config f.yaml` | 配置文件（YAML：font / workspace / preview_rows / privacy 等）；命令行优先 |
| `--update [--pre]` / `upgrade [--version V] [--pre] [--skills[=路径]]` | 检查更新 / 升级自身；`--skills` 顺带另存该版本的 SKILL.md（目录 / `.md` 文件 / 裸写法=当前目录），见文末 |

通用参数：`--tags a,b`、`--notes`、`--from <ref>`、`--preview-rows N`（Action 落盘的预览行数，默认 20，不影响 stdout）、`--no-record`（不记录、不可被引用）、`--load-mode auto|stream|full`、`--engine auto|sqlite|duckdb`、`--max-memory 2G`（`0` 关闭检查）、`-c config.yaml`。

## 6. SQL 规则（最常踩坑）

- **表名**：数据集名、`--source 别名=引用` 的别名，或**双引号/反引号**包裹的文件路径。
  单引号无效（那是字符串）。`--source` 只属于 query/pipeline/meminfo，convert 没有这个参数。
- **列名**：中文可以直接裸写（SQLite 与 DuckDB 都支持）：`SELECT 区域, SUM(销量) FROM orders GROUP BY 1`。
  只有含空格、以数字开头、或与关键字冲突时才用双引号（`"名称_2"`）。
- **类型是整列的**：数值列 `WHERE amount > 60`、`ORDER BY` 按数值比较；日期是 `TEXT`（ISO 格式），
  用 `date()` 等函数或字符串比较。带前导零、>15 位的 ID 保持 `string`。
- **引擎方言**：DuckDB 支持 `ROLLUP` / `CUBE` / `GROUPING SETS` / `QUALIFY`，SQLite 对这些报 `code 4`
  （`no such function` / `syntax error`）——这是引擎差异，不是工具缺陷。多维聚合或 `QUALIFY` 用 `--engine duckdb`。
- **沙箱**：只允许单条 `SELECT`/`WITH`；文件只能来自工作区、当前目录或 `--source`。
  不要尝试 `PRAGMA`/`ATTACH`/多语句。
- **结果体积**：超过 `--max-rows`（默认 10000）会报错，先聚合或加 `LIMIT`；要全量用 `--max-rows 0`
  或 `--format xlsx --output`。

## 7. 连表查询（JOIN）

同一个查询库里**每个数据源 = 一张表**。表名解析顺序：

1. `--source 别名=引用` 的别名（引用可为 `dataset:<名>` / `action:<id>` / `latest:<type>` / 文件路径）
2. FROM 位置上的**数据集名**（无需 `--source`）
3. **双引号/反引号**包裹的文件路径（单引号会报 no such column）

```bash
# 1) 先把各工作表转成数据集
dtool convert --input sales.xlsx --sheet 订单 --name orders
dtool convert --input sales.xlsx --sheet 客户 --name customers

# 2) 数据集名直接当表名（中文列名可裸写；含空格时才用双引号）
dtool query --sql 'SELECT c.客户名称, COUNT(*) AS 单数, SUM(o.金额) AS 总额
  FROM orders o JOIN customers c ON o.客户ID = c.客户ID
  GROUP BY 1 ORDER BY 总额 DESC LIMIT 5'

# 3) 显式别名，可与文件路径混用
dtool query --source o=dataset:orders --source c=.dtool/datasets/customers/data.json \
  --sql 'SELECT c.城市, SUM(o.金额) AS 总额 FROM o JOIN c ON o.客户ID = c.客户ID GROUP BY 1'
```

支持 `INNER`/`LEFT`/`CROSS JOIN`、`WITH`（CTE）、子查询、`UNION [ALL]`、窗口函数（`ROW_NUMBER`/`RANK`/`LAG` 等）。
沙箱只放行单条 `SELECT`/`WITH`，所以 `QUALIFY`（以 SELECT 开头）可用，而 `PIVOT` 等非 SELECT 语句会被拒。

要点与坑：

- **JOIN 键的类型**：列类型是整列推断的结果。带前导零或 >15 位的 ID 会保持 `TEXT`，两边都用字符串比较；
  类型不一致时用 `CAST(x AS TEXT)` 对齐。
- **NULL 语义**：`LEFT JOIN` 未匹配的一侧是 NULL，统计个数用 `COUNT(o.列)`，不要 `COUNT(*)`。
- **内存**：参与 JOIN 的每张表都要装进查询库，用量是各源之和（`--load-mode` 对每个源分别生效）。
- 写 SQL 前先 `datasets show <name>` 看列名与类型；表名拼错会提示「用 `--source` 绑定或把路径用双引号包裹」。

## 8. 结构确认（convert 的预演练与确认稿）

真实报表常有标题行、多级表头、合计行、合并单元格、「订单号其实是文本」。结构提取因此是**可反复的确认过程**：

| 步骤 | 命令 | 是否写产物 |
|------|------|-----------|
| 预演练 | `convert --input f.xlsx --rehearse` | 否（只读） |
| 看结构 | `convert --input f.xlsx --inspect > layout.json` | 否（只读） |
| 按确认稿转换 | `convert --input f.xlsx --layout layout.json --name N` | 是 |

- `--rehearse` 给出候选：表头行可能是第几行、用哪张工作表、每列还能是什么类型；每个候选带 `score`
  （0~1，只在**同组内**排序，不是概率）与 `effect`（列数 / 样本数据行 / 列宽不齐行数）。
- `--inspect` 给出当前方案（`Layout`）+ 两类信息：`questions`（需要人定的点）与 `warnings`（已自动处理的点）。
- `Layout` 关键字段：`header_row`（1 基，0=首个非空行）、`columns[]`（`name`/`type`/`source`/`confidence`/`evidence`）、
  `candidates`、`questions`、`warnings`。`evidence` 只有各类命中的**计数**，不含原值。
- **判定规则：只排除、不投票**。任一非空值不满足就淘汰该类型；只剩一个 → 自动定；剩多个 →
  写进 `columns[].candidates` 并作为 `type_ambiguity` 提问。目前有两类：
  0/1 列（`integer` 与 `boolean`，默认 integer，确认后可无损改成 boolean）；样本混写的列
  （默认取最安全的 `string`，保留原值，绝不让多数派改写少数派）。
- **强制类型必须无损**：`columns[].type` 只能改成整列确实满足的类型或 `string`。把混了 `N/A` 的列硬声明成
  `integer` 会被拒绝（code 2 并点名该列）——否则那些值会静默变成 0。
- 结合 excelize 的单元格数字格式做证据（`excel_date`/`excel_percent`/`excel_currency`）；
  格式与取值冲突时给出 `date_format` 问题，不替用户二选一。
- 带 `--layout` 时**不再自动探测**；结构与文件不符（版本 / 格式 / 列数 / 重名列）直接报错，
  改文件后重新 `--inspect` 即可。不要靠反复改参数试结构。
- `--converter` 选择转换器（默认 `auto` 按扩展名；当前只有 `xlsx`）；`auto` 没有兜底，认不出扩展名会报错。

## 9. 交付与读结果

- 把 `artifacts[].abs_path` 作为附件发给用户；需要重申交付内容时用 `dtool deliver <action_id>`
  （只给 `path`/`abs_path`/`mime`/`bytes`/`sha256` 与 `send`）。
- 回答「有哪些数据、有哪些列」用 `datasets show` 的结构字段，不依赖具体值。
- **跨引擎不要直接比 `sha256`**：没有稳定 `ORDER BY` 时 SQLite 与 DuckDB 的行序可能不同，
  产物内容等价但哈希不同。要比对就先加确定的 `ORDER BY`。
- 产物路径都相对项目目录：数据集 `.dtool/datasets/<name>/`，查询 `.dtool/outputs/queries/<id>/result.json`，
  图表 `.dtool/outputs/charts/<id>/`。

## 10. 常见对话 → 做法

| 用户说 | 做法 |
|--------|------|
| 直接描述目标与产物（"按区域汇总 `sales.xlsx` 并出柱状图，再给我 Top 5 客户 Excel"） | 别反问命令细节：`convert` 成数据集 → `datasets show` 确认列名 → `query` → `visualize --input latest:query` / `--format xlsx --output`，最后把产物路径报回去 |
| "格式很乱 / 表头不对 / 有合计行" | `convert --rehearse` 看候选 → `--inspect` 看问题 → 改 layout → `--layout` 转换 |
| "进度怎么样 / 刚才做了什么" | `actions list --limit 5`，用 `summary` 回答 |
| "为什么失败了" | `actions list --status failed --limit 1` → `actions show <id>`，读 `error`（含 `detail`/`hint`）；字段缺失时 `detail` 会列出可用列 |
| "任务像卡住了 / 进程被杀了" | `actions sync`：把进程已消失的 `running` 落盘为 `stale`（`error.code=5`），仍活着的列在 `running` 里；返回 `{"scanned","stale","stale_ids","running"}` |
| "刚才我按了 Ctrl+C，那次算失败吗" | 看 `error.code`：`5` = 被中断（重跑即可），`4` = 执行失败（要改输入） |
| "用上次的结果画图/导出" | `visualize --input latest:query ...`，不要重跑查询 |
| "按季度再拆一下" | 新 `query`，带 `--from action:<上次id>`（血缘），数据源用 `dataset:` 或 `--source` |
| "标记这个口径含税" | `actions annotate <id> --text "口径：含税" --by ai-agent` |
| "把之前标记含税的画成图" | `actions list` 找带 `annotations` 的 Action，再引用它 |
| 一次性掌握全部上下文 | `actions export --limit 50`，读导出的 JSON |

## 11. 错误与退出码

stdout 错误 JSON：`{"error","detail","hint","code","action_id"}`。

| code | 含义 | 该怎么办 |
|------|------|----------|
| `1` | 通用错误 | 看 `error` / `detail` |
| `2` | 参数 / 用法错误 | 改参数 |
| `3` | 引用或文件不存在 | 先 `datasets list` / 检查路径 |
| `4` | 执行失败（跑完了但出错：SQL 错、字段缺失、内存不足、超时…） | 按 `hint` 改输入或参数 |
| `5` | **被中断**（Ctrl+C / SIGTERM；或进程被强杀后收敛为 `stale`）——该步未完成、结果未知 | 直接重跑，数据与 SQL 没问题 |
| `6` | 强隐私拒绝读取数据 | 等用户把 `privacy` 改成 `normal`，不要绕过 |

**别把 4 和 5 混起来**：`4` 是「跑完了但结果不可用」，要改东西；`5` 是「半路没了」，重跑即可。
失败的步骤都会留下 `status: failed` 的 Action（`5` 亦然），可据此续写；进程被强杀时来不及写，见 `actions sync`。
`pipeline` 失败时 `detail` 指明失败的子步骤。

## 12. 图表与中文

图表默认字体不含中文。优先级：`--font` > 配置文件 `font` > 环境变量 `DTOOL_FONT` > 自动发现系统字体（`.ttf`/`.ttc`）。
输出 `warnings` 提示「未找到中文字体」或「字体缺少数字字形」时，让用户用 `--font`/配置指定 `.ttf`。

配置样例：

```yaml
font: /usr/share/fonts/truetype/arphic/uming.ttc   # 相对路径相对配置文件，支持 ~/
workspace: .dtool
preview_rows: 20
engine: auto      # auto（默认落到内存 SQLite）/ 已注册引擎名
db:
  engine:         # 各后端的配置（整棵子树交给后端，拼错键会直接报错）
    sqlite:
      cache_size_kb: 8192
privacy: strong   # 默认；只有用户本人可改成 normal
```

`privacy` 没有命令行开关、不读环境变量；Agent 不得修改它来绕过强隐私。

## 13. 内存与大数据

### 13.1 选档与峰值

JSON → 查询库有三种装入方式：`--load-mode auto`（默认）/ `stream` / `full`，`auto` 按内存预算选档：

1. 输入 <32MB 且整块解析预计峰值 ≤ 预算 80% → `full` + 内存库（最快）
2. 否则整块解析峰值 ≤ 预算 → `stream` + 内存库
3. 再不行 → `stream` + `--store disk`（峰值最低）
4. 连最省档都放不下 → 按 `--mem-policy`：`try`（默认）仍试最省档，`strict` 直接失败

峰值系数：`full+memory` ≈ 8–13×输入、`stream+memory` ≈ 1.4–2.4×、`stream+disk` ≈ 0.16×
（页缓存变成可回收的文件页）。stderr 与 Action 里写明选了哪档、为什么；结果里的 `strategy` 是实际档位、
`strategy_note` 是原因——突然变慢多半是降档了。

**1 核 2GB 能跑多大**（2026-10-10 黑盒实测）：
- `auto` 让峰值与数据量**解耦**：≤ ~1.1GB 输入走内存档（峰值 ≈ 文件 × 1.3–1.4），更大自动走磁盘档
  （峰值 ≈ 文件 × 0.02–0.05）；**3.4GB / 1830 万行跑通只用 59MB**——大文件只是慢，不是跑不动。
- 单核装入吞吐 ≈ 16MB/s（≈6.4s/100MB），按此估时间再调 `--timeout`。
- 估算门禁在 ~2.75GB 输入处翻转（阈值 1.4GB）；`strict` 会 0.0s 拒绝，`try` 实测仍能跑通。
  别用 `--max-memory 0` 硬扛远超内存的输入——那等于关掉看门狗，撞墙时是被内核杀掉而不是可读报错。
- 早期 1H2G 实测（默认 `auto`）：10 万行 2.4s；100 万行 19–29s；500 万行 1:29、604MB。

### 13.2 Excel（convert）的边界

- 两阶段、单次解析，峰值 ≈ 32MB + 文件 × 3，与行数无关：7.4MB/15 万行 → 47MB，17.8MB/40 万行 → 74MB，
  37.7MB/105 万行（Excel 行数上限）→ 55MB。所以边界是 Excel 的格式上限（单表 1,048,576 行），不是内存。
- 预检按「32MB + 文件 × 6」估算，超出预算会在转换前拦下并给出数字与退出口。
- convert 没有、也不需要 `--load-mode`：那组开关选的是 JSON → 查询库的装入方式，只对 `query` 生效
  （发给 convert 会被忽略并在 stderr 提示）。

### 13.3 失败与重试契约

| 看到 | 动作 |
|---|---|
| `code=0` + `strategy` | 完成；慢的话读 `strategy_note` |
| `code=4` + 提到「内存」 | 换更省档重跑一次：加 `--store disk`；仍失败就拆输入或提高 `--max-memory` |
| `code=5`（被杀） | `actions show <id>` 看 `strategy`，用更省档重跑一次；同一输入连续两次 `code=5` 就停止并上报 |

- 估算会**按本档历史上修**（`plan.rungs[].calibration`，8 倍封顶）：同一档曾「实测是预计的 N 倍」，
  下次就按 N 倍估。所以同一输入第一次可能撞看门狗（`code=4`），第二次自动换更省的档——
  **别把第一次失败当结论**，重跑一次再看。
- 选档历史在 `.dtool/plans/samples.json`（预计 vs 实测、耗时、成功与否 + 分位统计）；`--no-record` 不写。
  它让「试错」跨进程累积：崩过一次的档下次不再优先。
- 看门狗超时会先打印「内存超出预算（本进程已用 xx，预算 xx），正在中止」，再以 `code: 4` 失败，不会静默卡住。
- `--timeout` 只约束**查询阶段**（默认 60s），载入不计入。

### 13.4 预算来源与排查

- Linux 读 cgroup / 系统可用内存；Windows 读 **Job Object 内存上限**（`0x100` 进程级 / `0x2000` job 级，
  都设时取小值；存在即生效，按提交量判定）；其他平台（如 macOS）**不自动探测**——
  拿不到预算时预检与看门狗都不工作，容器/受限环境请显式给 `--max-memory`。
  放宽：`--max-memory 4G` / `--max-memory 0` / `DTOOL_MAX_MEMORY`。
- `dtool meminfo` 是排查入口（预算来源、原始探测字段、预检预演）；`DTOOL_DEBUG_MEMORY=1`
  把每个命令的判定过程打到 stderr（来源、预算、装入方式、预计 need、预检结论、看门狗阈值）。
- `meminfo` 的 `warnings` 是结论式的：Windows 上读不到 Job Object 上限时会说「预算回落到系统可用内存，
  请用 `--max-memory`」——**看到这句就别信 `budget`**，那只是本机空闲内存。
- `job_object.limit_unreadable=true`（标志位设了但值为 0）意味着预算不可信：工具会告警并自动排除整块解析档。
  顶到外部硬上限时可能看到「内存不足：… 引擎分配失败」（code 4，含 hint）；极端情况是 Go 的
  `fatal error: out of memory`（不可恢复），只会在没有预算可比对时发生。
- 性能结论有回归测试（环境/结果见 `docs/PERFORMANCE.md`），随 `go test ./...` 执行：
  `TestPerfLoadModeMemoryRatio`、`TestPerfConvertExcelMemory`；吞吐用 `make bench`，`DTOOL_BENCH_ROWS=N` 放大，
  `-short` 跳过。

### 13.5 看护进程与 stale

强杀（SIGKILL / TerminateProcess / 被 OOM 杀）后，dtool 启动的看护进程会在约 0.25s 内把这次命令遗留的
`running` 收敛为 `stale`（附原因、`code: 5`），无需再跑命令；`DTOOL_NO_REAPER=1` 可关闭
（受限沙箱不允许起子进程时），此时退回「下一条命令或 `actions sync` 收敛」。
整组被杀（如 `TerminateJobObject`）或断电时看护进程也会一起死，同样走那条兜底路径。

## 14. Windows 受限环境（方法库）

harness 若用 `CreateJobObjectW` + `AssignProcessToJobObject` 做沙箱，先记住这几条**实测结论**（Windows 10 19044）：

- **单核亲和是最强干扰变量**：`SetProcessAffinityMask(0x1)` 会让同一用例的峰值成倍上涨
  （12MB 输入实测 77MB → 156MB 量级）。核数必须作为独立变量记录，别把单核结论当默认结论。
- **`JOB_OBJECT_LIMIT_JOB_MEMORY`(0x2000) 在 Win10 19044 上不生效**：`SetInformationJobObject` 返回成功，
  但 `QueryInformationJobObject` 读回 `JobMemoryLimit=0`，子进程能超出限制运行。所以 dtool 读到
  `limit_unreadable` 是**正确行为**：预算回退系统可用内存、明确告警、并排除整块解析档。
  用例只应断言这条回退路径，不要断言能读到非零上限。
- **进程级 `0x100` 有效**：能设也能读回，是驱动预算与看门狗的那条路径。
- **看门狗中止点比阈值高 10~15MB**：采样窗口（硬上限下 100ms）内提交量还在涨，这是固有滞后，不是 bug；
  阈值 = 硬上限 × 0.85（余量）× 0.8（硬上限折扣）。
- **结构体要对齐**：`JOBOBJECT_EXTENDED_LIMIT_INFORMATION` x64 下 sizeof=144，
  `ProcessMemoryLimit@112`、`JobMemoryLimit@120`、`PeakProcessMemoryUsed@128`、`PeakJobMemoryUsed@136`；
  `SetInformationJobObject` 的信息类必须与结构匹配（class 9 用 Extended、class 2 用 Basic，
  混用报 `ERROR_BAD_LENGTH(24)`）。harness 里应加 sizeof/offset 自检。
- **`--store disk` 在 Windows 曾经不降峰值**：根因是磁盘档写死了 `temp_store(MEMORY)`，
  `GROUP BY` 的排序器留在内存里（聚合段 +42MB）。已修为 `temp_store(FILE)`，请复测。
- **`meminfo` 的预演与 `query` 的实际执行共用同一套选档逻辑**（含「上限不可信排除整块解析档」），
  两者应始终一致；不一致就是 bug。

## 15. 注意事项

- **先看结构再写 SQL**：`datasets show` 的 `type`/`role`/`distinct_count` 说明列类型与基数，`nullable`
  提示是否要处理 NULL；具体枚举值不在结构里，需要时等用户切到 `normal` 后用 `datasets values`。
- 前导零数字串（如 `00123`、手机号）和超过 15 位的整数保持 `string`；空表头自动命名 `col_N`，
  重复表头追加 `_N`，合并单元格仅首格有值——这些都会出现在 `warnings`。
- 图表上限 1000 个数据点；`line/bar/pie` 只接受 `png`/`svg`。
- `--no-record` 的结果之后无法用 `action:`/`latest:` 引用。
- 同名数据集被覆盖后，旧 Action 再读到的是新数据；要保留旧数据请换 `--name`。

## 16. 版本信息与自更新

`dtool version`（JSON）含 `version`、`channel`（stable/preview/dev/local）、`commit`、`commit_date`、
`branch`、`dirty`、`build_date`、`build_id`/`build_url`（CI 运行）、`builder`、`go`、`os`/`arch`；
`--short` 只输出版本号，`--deps` 附带依赖清单。反馈 bug 时先附上它。`dev-<id>` 是 main 的测试构建，
不在升级检测范围内。

```bash
dtool --update            # 只检查，返回 {current, channel, latest, update_available, release_url}
dtool --update --pre      # 同时考虑预览版（等价于 --channel preview）
dtool --update --channel dev   # main 的定时构建（每 4 小时；滚动发布只留最新一次）
dtool upgrade             # 升级到最新正式版（先校验 sha256，再替换）
dtool upgrade --pre       # 允许最新预览版
dtool upgrade --version 1.2.0          # 指定版本（可降级）；预览版写 1.2.0-preview.1
dtool upgrade --skills            # 顺带取该版本的 SKILL.md（写到当前目录）
dtool upgrade --skills=~/.dtool   # 或指定目录 / 文件：--skills=docs/ 或 --skills=agent.md
dtool upgrade --skills=~/.claude/skills/dtool/SKILL.md   # 直接装成平台技能包
```

渠道三选一：`stable`（默认）/ `preview`（`--pre`）/ `dev`（main 的定时构建，最多滞后 4 小时）。
`dev` 比的是**构建身份**而非版本大小（`dev-<run id>` 不是语义化版本）：发布里的 `dev-build.txt` 是权威来源，
与本地 `version`/`build_id` 一致即「已是最新」。`--version dev` 也可以（滚动发布只留最新一次，
`--version dev-<历史 run id>` 会被拒绝——要装特定构建请用那次 workflow 的 artifact）。
dev 构建由 CI 每 4 小时定时更新（也可手动触发；刻意不挂 push，免得每次提交都跑五平台构建），commit 未变则跳过。
`dev` 是移动 tag，已在版本号与发版基线计算中排除。
`--version` 与 `--pre` 不能同用；`--version` 与 `--channel` 也不能同用。
Windows 下运行中的 `dtool.exe` 会被改名为 `.old`，新文件改名就位，下次启动自动清理；
提示文件被占用时先关掉其它 dtool 进程。无写权限时 Linux/macOS 用 `sudo`，Windows 用管理员。
GitHub 限流时设置 `GITHUB_TOKEN`。

网络瞬断（`EOF` / `connection reset` / 5xx / 429）自动重试 3 次，间隔指数退避（500ms → 1s → 2s，上限 10s）；
仍失败时错误 JSON 的 `error` 会写「已尝试 N 次」，`hint` 给出 `https://github.com/dezhishen/dtool/releases`——
**AI Agent 遇到这种情况不必反复重试命令**，直接把 Release 资产下载地址告诉用户。
