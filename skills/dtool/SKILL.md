---
name: dtool
description: 本地数据 Pipeline CLI。用于把 Excel 转成可复用的数据集（JSON + Schema），用 SQL 查询，输出表格（CSV/Markdown/Excel）或图表（PNG/SVG），并把每一步记录为可追溯的 Action。当用户要分析 Excel 数据、做汇总/图表/导出，或追问"之前的结果/进度"时使用。
---

# dtool 使用指南（Agent Skill 正文）

单二进制、无外部依赖。所有命令的 **stdout 都是结构化 JSON**，失败时 stdout 仍输出错误 JSON、退出码非 0，人类可读信息写 stderr。

## 安装与升级

单二进制、无外部依赖（Linux/macOS/Windows × amd64/arm64）。从 Releases 下载对应平台的包，解包后即可用：

| 平台 | 包名 |
|------|------|
| Linux / macOS | `dtool_<版本>_<os>_<arch>.tar.gz` |
| Windows | `dtool_<版本>_windows_<arch>.zip`（内含 `dtool.exe`） |

```bash
V=v0.2.1                                             # 最新版本；预览版形如 v0.3.0-preview.1
# 自动取最新正式版：V=$(curl -sSL https://api.github.com/repos/dezhishen/dtool/releases/latest \
#   | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)   # 含预览版用 /releases（列表第一项）
base=https://github.com/dezhishen/dtool/releases/download/$V
curl -LO "$base/dtool_${V#v}_linux_amd64.tar.gz" && curl -LO "$base/checksums.txt"
sha256sum -c --ignore-missing checksums.txt          # macOS 用 shasum -a 256 -c
tar -xzf "dtool_${V#v}_linux_amd64.tar.gz" && install -m 0755 dtool ~/.local/bin/
dtool version                                        # 确认可用（含 version/channel/os/arch）
```

- 装完包内还有 `README.md`、`SKILL.md`、`LICENSE`、`THIRD_PARTY_NOTICES.md`。`SKILL.md` 就是本文件：
  它是按平台约定组织的技能包（仓库里在 `skills/dtool/SKILL.md`），解包后可直接丢进平台的技能目录，
  例如 `mkdir -p ~/.claude/skills/dtool && cp SKILL.md ~/.claude/skills/dtool/`。
- 升级自身：`dtool --update [--pre]` 只检查，`dtool upgrade [--pre]` 动手（下载后校验 `checksums.txt` 的 sha256 并跑一次自检，任一步失败都不改动现有文件）。二进制要放在**有写权限**的目录；网络瞬断按指数退避自动重试 3 次，彻底失败时按提示去 Releases 手动下载。
- **同时也更新本文件**：`dtool upgrade --skills[=路径]`——目录写 `<目录>/SKILL.md`，明确 `.md` 结尾的写该文件，
  裸 `--skills` 写当前目录；直接装进平台技能目录就是 `dtool upgrade --skills=~/.claude/skills/dtool/SKILL.md`
  （目录不存在会创建）。手册取自发布归档（与二进制同一次 sha256 校验），不会出现「二进制旧、手册新」；
  已经是最新版本时也能单独取（不碰二进制），`skills_changed` 说明内容有没有变。
  v0.2.0 及以前的归档里它叫 `skills.md`，两种名字都能取到，落点统一成 `SKILL.md`。
  反向不成立：v0.2.0 的旧二进制只按 `skills.md` 找，遇到 0.2.1+ 的归档会报「归档里没有
  skills.md」且不替换二进制——先不带 `--skills` 升级一次即可。
- **只有预览 tag 时加 `--pre`**，否则预览版不算可升级版本；不确定当前构建类型看 `dtool version` 的 `channel`（stable/preview/dev/local）。

## 核心概念

| 概念 | 说明 |
|------|------|
| 工作区 | 默认 `./.dtool`（`--workspace` 或 `-c config.yaml` 指定），所有状态都在里面，可整体搬迁 |
| 数据集 | `convert` 的产物，位于 `.dtool/datasets/<name>/`：`data.json`、`data.schema.json`（必有，含列类型/可空/枚举/样例/`updated_at`）、`dataset.json`。独立于 Action，可随时复用；同名重新转换会覆盖并刷新 `updated_at` |
| Action | 每次命令的完整记录（输入、产物、预览、错误、注释），位于 `.dtool/actions/`。读 Action 即可理解历史，无需重跑 |
| 引用 | `dataset:<name>`、`action:<id>`、`latest:<type>`（最近一次成功的 convert/query/…）、或文件路径 |

## 标准流程

```bash
# 1. 导入：Excel → 数据集（Schema 自动生成）
dtool convert --input sales.xlsx --name sales
dtool datasets show sales              # 位置 + Schema + 预览：先看 Schema 再写 SQL

# 2. 查询：数据集名直接当表名
dtool query --sql 'SELECT region, SUM(amount) AS total FROM sales GROUP BY region ORDER BY total DESC'

# 3. 出图 / 导出
dtool visualize --input latest:query --type bar --x region --y total --title "区域销量" --output out/sales.png
dtool query --sql '...' --format xlsx --output out/report.xlsx

# 一条龙（各步骤可选）
dtool pipeline --excel sales.xlsx --sql 'SELECT ... FROM data GROUP BY ...' --chart bar --x region --y total
dtool pipeline --excel sales.xlsx                           # 只转换并持久化
dtool pipeline --input dataset:sales --sql 'SELECT ... FROM data'   # 复用已有数据
```

## 命令速查

| 命令 | 要点 |
|------|------|
| `convert --input f.xlsx [--sheet S] [--name N]` | 仅 `.xlsx`；`--name` 缺省取文件名（指定 sheet 时追加 `_<sheet>`）；输出含 `data_file`/`schema_file`/`updated_at`/`warnings` |
| `meminfo [--source alias=文件]...` | 内存排查入口：预算来源（cgroup / Job Object / 系统可用内存 / `--max-memory`）、原始探测字段（`job_object`）、看门狗开关，以及按当前输入的**预检预演**（`verdict: ok/borderline/risky/forced`，以及选中的执行档 `plan.chosen` 与理由——预检与真正执行走同一套选档逻辑）。受限环境里「预检为什么没拦住」先跑它 |
| `datasets list \| show <name> \| delete <name>` | 查阅/删除数据集；回答"有哪些数据、长什么样"用这个 |
| `query --sql ... [--source 别名=引用]... [--format json\|csv\|markdown\|table\|xlsx] [--output f] [--max-rows N] [--timeout 60s] [--load-mode auto\|stream\|full]` | `xlsx` 必须配 `--output`；`--from <ref>` 仅记录血缘 |
| `visualize --input <ref> --type bar\|line\|pie\|table --x X --y Y [--format png\|svg] [--output f] [--font f.ttf]` | `table` 类型用 `--format md\|xlsx`，无需 x/y；y 必须是数值列 |
| `pipeline --excel f \| --input ref [--sql ...] [--chart ...]` | `--chart` 必须有 `--sql`；SQL 里用 `data` 指代上游数据 |
| `actions list [--limit N --type T --status S]` | 最新在前；状态含 `stale`（进程已死的 running，会在 ~0.25s 内被看护进程落盘收敛，见下） |
| `actions show <id> \| output <id> \| trace <id> \| annotate <id> --text ... --by ai-agent \| export \| reindex \| sync` | `annotate` 把用户口径/备注写进 Action；`sync` 显式收敛状态（把被强杀的 `running` 落盘为 `stale`，并列出真在跑的任务） |
| `--store auto\|memory\|disk` | SQLite 库落在哪：auto（按预算选档）/ memory（快）/ disk（峰值最低） |
| `--mem-policy try\|strict` | 所有档都预计超预算时：try 仍试最省档（失败记入 Action）/ strict 直接失败 |
| `-c, --config f.yaml` | 配置文件（YAML：font / workspace / preview_rows 等）；命令行参数优先 |
| `--sandbox` | 默认开启：SQL 只允许单条 SELECT，且只能读工作区 / 当前目录 / `--source` 文件；`--sandbox=false` 关闭 |
| `--update [--pre]` / `upgrade [--version V] [--pre] [--skills[=路径]]` | 检查更新 / 升级自身；`--skills` 顺带另存该版本的 SKILL.md（目录 / `.md` 文件 / 裸写法=当前目录）；网络瞬断自动重试 3 次（指数退避），失败时 hint 给出 Releases 页面，见文末 |

通用参数：`--tags a,b`、`--notes`、`--from <ref>`、`--preview-rows N`、`--no-record`（不记录、不可被引用）、`--load-mode auto|stream|full`、`--max-memory 2G`（`0` 关闭检查）、`-c config.yaml`。

## SQL 规则（最常踩坑）

- 表名：**数据集名**、`--source 别名=引用` 的别名，或**双引号/反引号**包裹的文件路径。**单引号无效**（那是字符串）。含中文/特殊字符的列名用双引号：`SUM("销量")`。
- 引擎是内存 SQLite：数值列按**整列**推断类型，`WHERE amount > 60`、`ORDER BY` 按数值比较；日期是 `TEXT`（ISO 格式），用 `date()` 等函数或字符串比较。
- 沙箱默认开启：只允许单条 `SELECT`/`WITH`；文件只能来自工作区、当前目录或 `--source`。不要尝试 `PRAGMA`/`ATTACH`/多语句。
- 结果过大会报错（`--max-rows`，默认 10000）：加 `LIMIT` 或先聚合。

## 连表查询（JOIN）

同一个内存 SQLite 里**每个数据源 = 一张表**，任意 JOIN 都能写。表名解析顺序：

1. `--source 别名=引用` 的别名（引用可为 `dataset:<名>` / `action:<id>` / `latest:<type>` / 文件路径）
2. FROM 位置上的**数据集名**（无需 `--source`）
3. **双引号**包裹的文件路径（单引号是字符串，会报 no such column）

```bash
# 1) 先把各工作表转成数据集
dtool convert --input sales.xlsx --sheet 订单 --name orders
dtool convert --input sales.xlsx --sheet 客户 --name customers

# 2) 数据集名直接当表名（中文/特殊字符列名用双引号）
dtool query --sql 'SELECT c."客户名称", COUNT(*) AS 单数, SUM(o."金额") AS 总额
  FROM orders o JOIN customers c ON o."客户ID" = c."客户ID"
  GROUP BY 1 ORDER BY 总额 DESC LIMIT 5'

# 3) 显式别名，可与文件路径混用
dtool query --source o=dataset:orders --source c=.dtool/datasets/customers/data.json \
  --sql 'SELECT c."城市", SUM(o."金额") AS 总额 FROM o JOIN c ON o."客户ID" = c."客户ID" GROUP BY 1'
```

支持 `INNER`/`LEFT`/`CROSS JOIN`、`WITH`（CTE）、子查询、`UNION [ALL]`、窗口函数
（`ROW_NUMBER`/`RANK`/`LAG` 等）；沙箱仍只允许**单条 `SELECT`/`WITH`**，`PRAGMA`/`ATTACH`/多语句会被拒。

要点与坑：

- **JOIN 键的类型**：列类型是**整列推断**的结果。带前导零或 >15 位的 ID 会保持 `TEXT`
  （如示例的 `客户ID`），两边都用字符串比较；类型不一致时用 `CAST(x AS TEXT)` 显式对齐。
- **NULL 语义**：`LEFT JOIN` 未匹配的一侧是 NULL，统计个数要用 `COUNT(o.列)`，不要用 `COUNT(*)`。
- **内存**：参与 JOIN 的每张表都要装进内存 SQLite，用量是各源之和（`--load-mode` 对每个源分别生效）。
- **结果体积**：`--max-rows`（默认 10000）会拦截大结果，先聚合或加 `LIMIT`；要全量就 `--max-rows 0`
  或 `--format xlsx --output`。
- 写 SQL 前先 `dtool datasets show <name>` 看列名与类型；表名拼错会提示「用 `--source` 绑定或把路径用双引号包裹」。

## 读取结果的方式

- 默认只读 Action 的 `output.preview`（前 20 行）+ `columns` + `summary`；`preview_truncated: true` 才需要读完整产物（`actions output <id>`）。
- 产物路径都是相对项目目录的：数据集 `.dtool/datasets/<name>/`，查询 `.dtool/outputs/queries/<id>/result.json`，图表 `.dtool/outputs/charts/<id>/`。

## 对话场景

| 用户说 | 做法 |
|--------|------|
| 直接描述目标与产物（"按区域汇总 `sales.xlsx` 并出柱状图，再给我 Top 5 客户 Excel"） | 别反问命令细节：`convert` 成数据集 → `datasets show` 确认列名 → `query` → `visualize --input latest:query` / `--format xlsx --output`，最后把产物路径报回去 |
| "进度怎么样 / 刚才做了什么" | `actions list --limit 5`，用 `summary` 回答 |
| "为什么失败了" | `actions list --status failed --limit 1` → `actions show <id>`，读 `error`（含 `detail`/`hint`）；字段缺失时 `detail` 会列出可用列 |
| "任务像卡住了 / 进程被杀了" | `actions sync`：把进程已消失的 `running` 落盘为 `stale`（含原因，`error.code=5`），仍活着的列在 `running` 里；返回 `{"scanned","stale","stale_ids","running"}` |
| "刚才我按了 Ctrl+C，那次算失败吗" | `actions list --status failed --limit 1` → 看 `error.code`：`5` = 被中断（重跑即可），`4` = 执行失败（要改输入） |
| "用上次的结果画图/导出" | `visualize --input latest:query ...`，不要重跑查询 |
| "按季度再拆一下" | 新 `query`，带 `--from action:<上次id>`（血缘），数据源用 `dataset:` 或 `--source` |
| "标记这个口径含税" | `actions annotate <id> --text "口径：含税" --by ai-agent` |
| "把之前标记含税的画成图" | `actions list` 找带 `annotations` 的 Action，再引用它 |
| 一次性掌握全部上下文 | `actions export --limit 50`，读导出的 JSON |

## 错误与退出码

stdout 错误 JSON：`{"error","detail","hint","code","action_id"}`。退出码：

| code | 含义 | 该怎么办 |
|------|------|----------|
| `1` | 通用错误 | 看 `error` / `detail` |
| `2` | 参数 / 用法错误 | 改参数 |
| `3` | 引用或文件不存在 | 先 `datasets list` / 检查路径 |
| `4` | 执行失败（跑完了但出错：SQL 错、字段缺失、内存不足、超时…） | 按 `hint` 改输入或参数 |
| `5` | **被中断**（Ctrl+C / SIGTERM；或进程被强杀后收敛为 `stale`）——该步未完成、结果未知 | 直接重跑，数据与 SQL 本身没问题 |

**别把 4 和 5 混起来**：`4` 是「跑完了但结果不可用」，要改东西；`5` 是「半路没了」，
重跑即可——所以 AI 不该为 `5` 去猜 SQL 哪里写错了。

失败的步骤都会留下 `status: failed` 的 Action（`5` 亦然，`error.code=5`），可据此续写，
无需从头来；进程被强杀时来不及写，见 `actions sync`。`pipeline` 失败时 `detail` 指明失败的子步骤。

## 图表与中文

图表默认字体不含中文。字体优先级：`--font` > 配置文件 `font` > 环境变量 `DTOOL_FONT` > 自动发现的系统字体（`.ttf`/`.ttc`）。输出里的 `warnings` 提示"未找到中文字体"或"字体缺少数字字形"时，请让用户用 `--font`/配置指定 `.ttf`。配置文件示例：

```yaml
font: /usr/share/fonts/truetype/arphic/uming.ttc   # 相对路径相对配置文件，支持 ~/
workspace: .dtool
preview_rows: 20
```

## 装入方式与大数据的现实边界

JSON → 内存 SQLite 有两种装入方式：`--load-mode auto`（默认）/ `stream` / `full`。

- `auto`（默认）按**内存预算**选档，不用自己算：① 输入 <32MB 且整块解析预计峰值 ≤ 预算 80% →
  `full` + 内存库；② 否则整块解析峰值 ≤ 预算 → `stream` + 内存库；③ 再不行 → `stream` +
  `--store disk`（峰值最低）；④ 连最省档都放不下 → 按 `--mem-policy`（`try` 默认仍试最省档，
  `strict` 直接失败）。stderr 与 Action 里会写明选了哪档、为什么，出问题先看那里。
- 峰值内存：`full` ≈ 文件大小 × 13；`stream` ≈ ×2。
- **1 核 2GB 能跑多大**（2026-10-10 黑盒实测，**内存为准、耗时仅供参考**）：`auto` 按预算自动降档，
  峰值与数据量**解耦**——≤ ~1.1GB 走内存档（峰值 ≈ 文件 × 1.3–1.4），更大自动走磁盘档
  （峰值 ≈ 文件 × 0.02–0.05）：**3.4GB / 1830 万行跑通只用 59MB**。所以**大文件只是慢，不是跑不动**：
  单核装入吞吐 ≈ 16MB/s（≈6.4s/100MB），按这个估时间再调 `--timeout`。
- 1 核 2GB 的**估算门禁**在 ~2.75GB 输入处翻转（阈值 1.4GB）：默认 `--mem-policy try` 仍会按最省档
  试一次（实测成功）；`--mem-policy strict` 才 0.0s 拒绝。要不要冒险看 `meminfo` 的 `plan.reason`。
  别用 `--max-memory 0` 硬扛远超内存的输入——那等于关掉看门狗，撞墙时会是被内核杀掉而不是可读报错。
- 早期 1H2G 实测（默认 `auto`）：10 万行 2.4s；100 万行 19–29s；500 万行 1:29、604MB。
- **Excel（`convert`）也是流式的**：两阶段、单次解析，峰值 ≈ 32MB + 文件 × 3（实测 7.4MB/15 万行 → 47MB，17.8MB/40 万行 → 74MB，**37.7MB/105 万行（Excel 行数上限）→ 55MB**），与行数无关——
所以 xlsx 的边界是 Excel 的格式上限（单表 1,048,576 行），不是内存。它没有、也不需要 `--load-mode`——那组开关选的是 JSON → 内存 SQLite 的装入方式，只对 `query` 生效。预检按「32MB + 文件 × 6」估算，超出预算才会在转换前拦下并给出数字与退出口。
- `--timeout` 只约束**查询阶段**（默认 60s），载入耗时不计入。若内存充足、只想要速度，可用 `--load-mode full`（只解析一遍，更快）。
- 内存不足时会**快速失败并说明原因**（含「预计需 xx、可用 xx」与 `--max-memory 0` 退出口），不会静默被杀；这类失败同样留下 `failed` 的 Action。处置建议按场景给：`query` 能切 `--load-mode stream`（整块解析峰值 ≈ 文件 × 13）；`convert` 没有开关可切，只能缩小输入（拆文件、裁列）或放宽预算——把 `--load-mode` 发给它不报错也不生效（CLI 会在 stderr 提示「已忽略」）。
- 看到 stderr 的「载入 xxx.json（…，流式解析，预计需约 xx 内存）...」说明正在载入；若进程随后消失，就是内存不够。
- 强杀（SIGKILL / TerminateProcess / 被 OOM 杀）后，dtool 启动的**看护进程**会在约 0.25s 内把
  这次命令遗留的 `running` 收敛为 `stale`（附原因、`code: 5`），无需再跑任何命令；
  `DTOOL_NO_REAPER=1` 可关闭（受限沙箱不允许起子进程时），此时退回「下一条命令或 `actions sync` 收敛」。
  整组被杀（如 `TerminateJobObject`）或断电时看护进程也会一起死，同样退回那条兜底路径。
- Windows 上的预检与看门狗按**提交量**（commit charge）判定，因为 Job Object 的进程内存上限
  本身就是提交上限；Linux 仍按 RSS。
- 内存超出预算时看门狗会先打印「内存超出预算（本进程已用 xx，预算 xx），正在中止」，再以 `code: 4` 失败——不会静默卡住。显式 `--load-mode full` 时也是逐元素解码、可被中止，长文件不会再出现「几十秒没有任何输出」。
- **内存上限决定「选哪一档」，不是「过/不过」**：`full+memory`（最快、峰值 ≈ 8–13×输入）
  → `stream+memory`（≈ 1.4–2.4×）→ `stream+disk`（≈ 0.16×，页缓存变成文件页、可回收）。
  实测 123MB 输入在 1 核 / 256MB 硬上限下：内存档峰值 176MB，磁盘档只有 20MB，耗时一样。
  所以受限环境里默认动作是「换个档照跑」。
- **结果里的 `strategy` 告诉你用了哪一档**，`strategy_note` 说明为什么（预算、阈值、历史成功率）。
  突然变慢多半是降档了；需要快就 `--load-mode full`（单档强制）。
- **失败与重试的契约**：
  | 看到 | 动作 |
  |---|---|
  | `code=0` + `strategy` | 完成；慢的话读 `strategy_note` 解释原因 |
  | `code=4` + 提到「内存」 | 换更省档重跑一次：加 `--store disk`；仍失败就拆输入或提高 --max-memory |
  | `code=5`（被杀） | `actions show <id>` 看 `strategy`，再用更省档重跑一次；同一输入连续两次 `code=5` 就停止并上报 |
  默认策略 `try` 会在预计放不下时**仍试一次**（宁可慢，不要崩），`--mem-policy strict` 才是直接失败。
- 需要放宽/关闭检查：`--max-memory 4G` / `--max-memory 0`，或 `DTOOL_MAX_MEMORY` 环境变量。
- 估算会**按本档历史上修**（`plan.rungs[].calibration`，8 倍封顶）：同一档历史显示过
  「实测是预计的 N 倍」时，下次就按 N 倍估。所以同输入第一次可能撞看门狗（`code=4`），
  第二次就会自动换到更省的档——**别把第一次的失败当成结论**，重跑一次再看。
- Windows 上 `job_object.limit_unreadable=true`（标志位设了但值读回 0）意味着预算只是
  本机空闲内存、不可信：工具会警告并自动排除整块解析档；要准确就 `--max-memory` 或 `--store disk`。
- 选档历史在 `.dtool/plans/samples.json`（预计 vs 实测峰值、耗时、成功与否 + 派生分位统计）；
  `--no-record` 时不写。它让「试错」跨进程累积：崩过一次的档下次不会再被优先选中。
- 排查「预检/看门狗到底在不在工作」：`dtool meminfo [--source alias=文件]`（预算来源、原始探测字段与预检预演）；
  `DTOOL_DEBUG_MEMORY=1` 把每个命令的判定过程打到 stderr（来源、预算、装入方式、预计 need、预检结论、看门狗阈值）。
- 预算来源随平台不同：Linux 读 cgroup/系统可用内存，Windows 读 **Job Object 内存上限**
  （`0x100` 进程级 / `0x2000` job 级，两者都设时取小值；存在即生效），
  其他平台（如 macOS）**不自动探测**——拿不到预算时预检与看门狗都不工作，所以容器/受限环境里请显式给
  `--max-memory`。顶到外部硬上限时可能看到「内存不足：…SQLite 分配失败」（code 4，含 hint），
  极端情况下是 Go 的 `fatal error: out of memory`（不可恢复）——后者只会在没有预算可比对时发生。
- `dtool meminfo` 的 `warnings` 是结论式的：Windows 上读不到 Job Object 上限时它会说
  「预算回落到系统可用内存，请用 `--max-memory`」——**看到这句就别相信 `budget` 里的数字**，
  那只是本机空闲内存，不是沙箱允许你用的量。
- 性能结论有回归测试（环境/方式/结果见 `docs/PERFORMANCE.md`），随 `go test ./...` 执行：`TestPerfLoadModeMemoryRatio`（流式峰值须低于整块解析 1.3 倍以上）、`TestPerfConvertExcelMemory`（转换峰值不得超过预检倍率）。吞吐用 `make bench` 看，`DTOOL_BENCH_ROWS=N` 放大，`-short` 跳过这些回归。

## Windows 受限环境回归（方法库）

harness 若用 `CreateJobObjectW` + `AssignProcessToJobObject` 做沙箱，先记住这几条**实测结论**（Windows 10 19044）：

- **单核亲和是最强干扰变量**：`SetProcessAffinityMask(0x1)` 会让同一个用例的峰值成倍上涨
  （12MB 输入实测 77MB → 156MB 量级）。核数必须作为独立变量记录，别把单核结论当默认结论。
- **`JOB_OBJECT_LIMIT_JOB_MEMORY`(0x2000) 在 Win10 19044 上不生效**：`SetInformationJobObject`
  返回成功，但 `QueryInformationJobObject` 读回 `JobMemoryLimit=0`，子进程能超出限制运行。
  所以 dtool 读到 `limit_unreadable`（标志位设了、值是 0）**是正确行为**：预算回退系统可用内存、
  明确告警、并排除整块解析档。用例只应断言这条回退路径，不要断言能读到非零上限。
- **进程级 `0x100` 有效**：能设也能读回，是驱动预算与看门狗的那条路径。
- **看门狗中止点比阈值高 10~15MB**：采样窗口（硬上限下 100ms）内提交量还在涨，这是固有滞后，
  不是 bug；阈值 = 硬上限 × 0.85（余量）× 0.8（硬上限折扣）。
- **结构体要对齐**：`JOBOBJECT_EXTENDED_LIMIT_INFORMATION` x64 下 sizeof=144，
  `ProcessMemoryLimit@112`、`JobMemoryLimit@120`、`PeakProcessMemoryUsed@128`、`PeakJobMemoryUsed@136`；
  `SetInformationJobObject` 的信息类必须与结构匹配（class 9 用 Extended、class 2 用 Basic，
  混用报 `ERROR_BAD_LENGTH(24)`）。建议 harness 里加 sizeof/offset 自检。
- **`--store disk` 在 Windows 曾经不降峰值**：根因是磁盘档写死了 `temp_store(MEMORY)`，
  `GROUP BY` 的排序器留在内存里（聚合段 +42MB）。已修为 `temp_store(FILE)`，请复测。
- **`meminfo` 的预演与 `query` 的实际执行共用同一套选档逻辑**（含「上限不可信排除整块解析档」），
  两者应始终一致；不一致就是 bug。

## 注意事项

- **先看 Schema 再写 SQL**：`datasets show <name>` 的 `enum` 给出取值空间，`type` 给出列类型，`nullable` 提示是否要处理 NULL。
- 前导零数字串（如 `00123`、手机号）和超过 15 位的整数会保持 `string`；空表头自动命名 `col_N`，重复表头追加 `_N`，合并单元格仅首格有值——这些都会出现在 `warnings`。
- 图表上限 1000 个数据点；`line/bar/pie` 只接受 `png`/`svg`。
- `--no-record` 的结果之后无法用 `action:`/`latest:` 引用。
- 同名数据集被覆盖后，旧 Action 再读到的是新数据；需要保留旧数据请换 `--name`。

## 查看版本与构建信息

`dtool version`（JSON）含 `version`、`channel`（stable/preview/dev/local）、`commit`、`commit_date`、`branch`、`dirty`、`build_date`、`build_id`/`build_url`（CI 运行）、`builder`、`go`、`os`/`arch`；`--short` 只输出版本号，`--deps` 附带依赖清单。排查问题或反馈 bug 时先附上它。`dev-<id>` 是 main 分支的测试构建，不在升级检测范围内。

## 更新自身

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
`dev` 渠道比的是**构建身份**而不是版本大小（`dev-<run id>` 不是语义化版本）：发布里的
`dev-build.txt` 是构建号的权威来源，和本地 `version`/`build_id` 一致就是「已是最新」。
想升到 dev：`dtool upgrade --channel dev`；`--version dev` 也可以（滚动发布**只保留最新一次**，
`--version dev-<历史 run id>` 会被拒绝——要装特定构建请用那次 workflow 的 artifact）。
dev 构建由 CI **每 4 小时**定时更新（也可在 Actions 页面手动触发；刻意不挂 push，免得每次提交都跑一轮
六平台构建），commit 未变则跳过。
`dev` 是移动 tag，已在版本号与发版基线计算中排除——本地构建不会因此变成 dev 渠道。
`--version` 与 `--pre` 不能同时使用；`--version` 与 `--channel` 也不能同时使用。Windows 下运行中的 `dtool.exe` 会被改名为 `.old`，新文件改名就位，下次启动自动清理；若提示文件被占用，请关闭其他 dtool 进程后重试。无写权限时在 Linux/macOS 用 `sudo`，Windows 用管理员。GitHub 限流时设置 `GITHUB_TOKEN`。

网络瞬断（`EOF` / `connection reset` / 5xx / 429）会自动重试 3 次，间隔指数退避（500ms → 1s → 2s，上限 10s）；仍失败时错误 JSON 的 `error` 会写明「已尝试 N 次」，`hint` 里给出 `https://github.com/dezhishen/dtool/releases`——**AI Agent 遇到这种情况不必反复重试命令**，直接把 Release 资产下载地址告诉用户即可。
