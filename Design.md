# 设计文档：本地数据 Pipeline CLI 工具

## 1. 项目概述

### 1.1 目标

构建一个**纯 Go、无 CGO、单二进制**的命令行工具，用于本地数据处理 Pipeline：

1. **导入**：将 Excel 表格转换为 JSON 数据与 JSON Schema（Schema 从数据推断，供 AI 快速理解结构）。
2. **查询**：基于 SQL 查询大量 JSON 数据（底层把 JSON 载入内存 SQLite，使用纯 Go 的 modernc.org/sqlite 驱动）。
3. **输出**：将查询结果渲染为表格（CSV / Markdown / JSON）或图表（PNG / SVG）。
4. **记录**：每一步的完整结果（输入、产物、数据预览、Schema、错误）都持久化为 **Action**，形成可追溯的时间线。
5. **协作**：用户可通过 AI 对话，基于已有 Action 记录**追加、修正、派生**新的步骤，无需从头执行。

### 1.2 设计原则

- **单二进制**：所有功能编译进一个可执行文件，无外部运行时依赖。
- **纯 Go**：`CGO_ENABLED=0` 可编译，方便交叉编译与分发。
- **契约稳定**：每个子命令的 stdout 输出为结构化 JSON，错误信息包含可操作的上下文。
- **Action 自包含**：每条记录包含足够上下文，使 AI 无需重新执行即可理解并续写。
- **可追加**：Action 不覆盖、可注释、可派生，支持对话式迭代。
- **内存优先**：支持从内存缓冲区直接处理数据，减少中间文件落盘。

---

## 2. 技术选型

| 功能 | 选型 | 说明 |
|------|------|------|
| CLI 框架 | `spf13/cobra` | 成熟、子命令支持好、自动生成帮助 |
| Excel 读取 | `xuri/excelize/v2` | 纯 Go，支持 .xlsx |
| Schema 推断 | 自研（约 150 行） | 从转换后的 JSON 数据推断，输出 AI 友好格式 |
| SQL 引擎 | `modernc.org/sqlite`（内存库，自研载入） | 纯 Go；按整列推断类型建表，保证数值比较/排序正确 |
| 配置 | `gopkg.in/yaml.v3` | `-c config.yaml`，严格模式拒绝未知键 |
| SQLite 驱动 | `modernc.org/sqlite` | 纯 Go，无 CGO |
| 图表生成 | `vicanso/go-charts/v2` | 纯 Go，支持折线/柱状/饼图等，输出 PNG/SVG；中文需要 CJK 字体，见 8.5 |
| 表格输出 | 标准库 + 自定义格式化 | CSV / Markdown / JSON |
| Action 存储 | 文件系统 + JSON | 每个 Action 一个文件，简单可读 |
| ID 生成 | `oklog/ulid/v2` | Action 唯一标识，时间有序 |
| Go 版本 | 最新稳定版（`go.mod` 中 `go` 指令取当前最新） | 模块路径 `github.com/dezhishen/dtool` |

**引擎选型结论（spike 实测）**：最初计划用 trdsql，但实测发现它把 JSON 数值（`json.Number`）一律建成 `TEXT` 列，且仅按首行推断类型，导致 `WHERE amount > 60`、`ORDER BY` 变成文本比较，结果错误。因此改为**自研载入**：把 JSON 数组文件读入内存 SQLite（`modernc.org/sqlite`，`CGO_ENABLED=0` 可编译），按**整列**推断类型：全整数 `INTEGER`、全数值 `REAL`、其余 `TEXT`（布尔存 0/1，嵌套对象/数组存 JSON 文本），首行为 null 也不影响。顺带的好处：SQLite 从不触碰文件系统，沙箱更简单可靠。

**关于 Schema 生成的说明**：`invopop/jsonschema` 等库适用于**从 Go struct 反射生成 Schema**（结构编译期已知的场景）。本工具的场景是**运行时从 Excel 读取、结构未知**，因此 Schema 必须在 `convert` 过程中从数据本身推断。自研推断器代码量小、可精确控制输出格式，更适合 AI 读取。

---

## 3. 总体架构

```
┌──────────────────────────────────────────────────────────────┐
│                     AI Agent / 用户                            │
│   执行 CLI + 读取 Action + 对话式追加/派生/注释                │
└───────────────┬──────────────────────────────────────────────┘
                │
                ▼
┌──────────────────────────────────────────────────────────────┐
│                    dtool (单二进制)                            │
│                                                              │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌─────────┐     │
│  │ convert  │  │  query   │  │visualize │  │ pipeline│     │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘  └────┬────┘     │
│       │             │             │             │           │
│       └─────────────┴─────────────┴─────────────┘           │
│                          │                                   │
│                          ▼                                   │
│              ┌────────────────────────┐                      │
│              │   Action Recorder      │                      │
│              │  （记录每步完整结果）  │                      │
│              └───────────┬────────────┘                      │
│                          │                                   │
└──────────────────────────┼───────────────────────────────────┘
                           │
                           ▼
              ┌────────────────────────────┐
              │  Workspace 目录             │
              │  .dtool/                    │
              │   ├── actions/              │
              │   │   ├── <action-id>.json  │
              │   │   └── ...               │
              │   ├── outputs/              │
              │   │   ├── data.json         │
              │   │   ├── data.schema.json  │
              │   │   ├── chart.png         │
              │   │   └── ...               │
              │   └── index.json            │
              └────────────────────────────┘
```

**关键点**：所有命令共享同一个 **Workspace**，每一步的完整结果都写入 Action。AI 读取 Action 即可掌握上下文，用户通过对话让 AI 基于既有 Action 续写新的步骤。

---

## 4. Workspace 与 Action 模型

### 4.1 Workspace 目录结构

工具默认在当前工作目录下创建 `.dtool/` 作为工作区，可通过 `--workspace` 指定：

```
.dtool/
├── workspace.json            # 工作区元信息
├── actions/                  # 所有 Action 记录
│   ├── 01HX...A.json
│   ├── 01HX...B.json
│   └── ...
├── datasets/                 # 数据集：独立于 Action 的一等实体，随时可被任意 Action 引用
│   ├── 销量表/
│   │   ├── data.json
│   │   ├── data.schema.json  # 必有：列类型/可空/枚举/样例 + updated_at 更新日期
│   │   └── dataset.json      # 元数据：名称、来源、创建/更新时间、记录数、列、产出它的 action_id
│   └── hr/...
├── outputs/                  # Action 产物，每次执行一个 <action-id> 目录，互不覆盖
│   ├── queries/01HX...B/result.json   # query（及 --output 的格式化副本）
│   └── charts/01HX...E/chart.png      # visualize：chart.png / chart.svg / table.md|xlsx
├── index.json                # Action 索引（可由 actions/ 重建，非唯一真相）
└── .lock                     # 写索引/Action 时的文件锁（flock），防并发损坏
```

**约束**：

- **数据集与 Action 解耦**：数据集存放在 `datasets/<name>/`，路径里没有 action id，也不在 `outputs/` 下；它由 `convert` 创建/更新，但生命周期独立——可被任意 Action 引用，删除所有 Action 记录后仍可用，用 `dtool datasets delete` 单独删除。Action 只通过 `dataset:<name>` 引用，并在 `derived_from` 中记录产出当前数据的 Action。
- **命名**：`--name`，缺省取输入文件名（指定 `--sheet` 时追加 `_<sheet>`）；非字母数字/中文/`-_.` 的字符替换为 `_`，防止路径穿越。
- **更新语义**：数据集没有版本概念。同名重新 `convert` 会**覆盖**数据与 Schema，并刷新 `updated_at`（先写暂存目录，再在锁内 rename，避免读到半截数据）。**Schema 是数据集的必备部分**，缺失则不会提交。需要保留旧数据请换一个 `--name`。
- **直接用作表名**：SQL 中 `FROM`/`JOIN` 后的名称若是数据集名则自动绑定（`SELECT ... FROM sales`），无需 `--source`；显式 `--source` 优先。
- **Action 产物**：`outputs/queries/<action-id>/`、`outputs/charts/<action-id>/`；文档后文出现的 `result.json` 等均指对应目录下的文件。`--output` 指定外部路径时直接写到该处，并在 Action 的 `files` 中记录。
- **查阅**：`dtool datasets list`（名称、更新时间、记录数、列、文件位置）、`dtool datasets show <name>`（位置 + Schema + 预览）、`dtool datasets delete <name>`，AI 在对话中据此回答"有哪些数据、长什么样"。
- `index.json` 只是缓存，损坏或缺失时由 `dtool actions reindex` 从 `actions/` 重建。
- 所有写操作持有 `.lock`；Action 文件只会被其所属进程与 `annotate` 修改。
- 所有路径记录为相对工作区的相对路径，保证工作区可整体搬迁。

**为什么用文件系统而不是数据库？**

- AI 可以直接 `cat` / `read_file` 读取，无需额外工具。
- 用户可手动查看、diff、版本控制。
- 无外部依赖，符合单二进制目标。

### 4.2 Action 数据结构（自包含）

每条 Action 记录必须**自包含**：包含足够的输入、输出、产物、预览和上下文，使 AI 无需重新执行即可理解这一步做了什么、结果如何、下一步可以做什么。

```json
{
  "id": "01HX7Z8K2M9PQR3STUVWXYZABC",
  "type": "query",
  "status": "success",
  "started_at": "2026-10-08T14:23:01.123Z",
  "finished_at": "2026-10-08T14:23:01.456Z",
  "duration_ms": 333,
  "command": "dtool query --sql \"SELECT region, SUM(amount) AS total FROM 'data.json' GROUP BY region\" --format json",
  "input": {
    "sql": "SELECT region, SUM(amount) AS total FROM 'data.json' GROUP BY region",
    "sources": [".dtool/datasets/sales/data.json"],
    "format": "json"
  },
  "output": {
    "files": [".dtool/outputs/queries/01HX7Z8K2M9PQR3STUVWXYZABC/result.json"],
    "row_count": 2,
    "columns": ["region", "total"],
    "schema_ref": ".dtool/datasets/sales/data.schema.json",
    "preview": [
      {"region": "North", "total": 15000},
      {"region": "South", "total": 23000}
    ],
    "preview_truncated": false,
    "summary": "查询返回 2 行"
  },
  "parent_id": "01HX7Z8K2M9PQR3STUVWXYZ000",
  "derived_from": null,
  "annotations": [
    {
      "at": "2026-10-08T14:25:00Z",
      "by": "user",
      "text": "这个结果后面还要按季度拆分"
    }
  ],
  "error": null,
  "metadata": {
    "user": "ai-agent",
    "tags": ["sales-report"],
    "notes": "按地区汇总销售额"
  }
}
```

**字段说明**：

| 字段 | 说明 |
|------|------|
| `id` | ULID，时间有序，便于排序 |
| `type` | `convert` / `query` / `visualize` / `pipeline` |
| `status` | `success` / `failed` / `running` |
| `started_at` / `finished_at` | ISO 8601 时间戳 |
| `duration_ms` | 执行耗时 |
| `command` | 完整命令行，便于复现 |
| `input` | 结构化输入参数 |
| `output` | 产物路径、行数、列名、**数据预览**、Schema 引用 |
| `parent_id` | 若属于某个 pipeline，指向上游 Action |
| `derived_from` | 若由对话派生，指向被派生的源 Action |
| `annotations` | **对话式补充**：用户/AI 追加的注释 |
| `error` | 失败时的错误信息 |
| `metadata` | 用户/AI 附加的标签与备注 |

### 4.3 数据预览策略

为了让 AI 无需读取完整产物即可理解结果，`output.preview` 应包含：

- 默认前 **20 行**数据（可通过 `--preview-rows` 调整）。
- 若结果行数超过预览行数，`preview_truncated: true`，AI 会知道需要读取完整文件。
- 若结果为空，`preview: []` 且 `summary` 说明"查询返回 0 行"。
- 对于 `convert`，预览为前 20 条记录 + 列名列表。
- 对于 `visualize`，预览为生成的图表元信息（类型、轴字段、数据点数量）。

这样，AI 在大多数对话场景下**只需读取 Action 文件，无需打开产物文件**，即可回答"结果是什么"、"有哪些字段"、"能不能画饼图"等问题。

### 4.4 Action 生命周期

```
started ──► running ──► success        退出码 0
                    ├─► failed         退出码 4（执行失败）/ 5（被 Ctrl+C、SIGTERM 中断）
                    └─► stale          退出码 5：进程被强杀，由看护进程（或下一条命令 / actions sync）收敛
```

- **started**：命令开始执行时立即写入 Action 文件（`status: "running"`）。
- **success/failed**：执行结束时更新同一文件。
- 中途崩溃时，Action 停留在 `running`，AI 可通过 `status` 识别异常任务。
- Action 记录 `pid`；`running` 且进程已不存在时，`actions list` 将其显示为 `stale`，避免与真实运行中的任务混淆。
- 更进一步，每次命令启动（`openWorkspace`）会调用 `Recorder.Reconcile` 做一次回收：把陈旧的
  `running` 在 **Action 文件与 `index.json` 上一起落盘为 `stale`**，并补一条可读原因（「进程已消失
  （PID n 不存在），任务被中断，结果未知」）。否则直接读 `.dtool/actions/<id>.json` 的脚本、AI 或
  git diff 看到的仍是 `running`，会误以为任务还在跑。回收是幂等的，只在确有陈旧条目时才写盘。
- 捕获 SIGINT/SIGTERM（`signal.NotifyContext`），把当前步骤以普通错误收尾：状态 `failed`、
  `error.code=5`（interrupted），消息写明阶段（「已中断：载入阶段未完成（收到 Ctrl+C / SIGTERM）」），
  并附 `hint`「重跑该命令即可」。转换（`convert`）与 JSON 装入（`query`）都在行循环里检查 ctx，
  所以 Ctrl+C 立刻生效，不会「按了没反应、等整表转完」。
- 强杀（SIGKILL / TerminateProcess / OOM / 断电）无法捕获：状态停在 `running`。为此每条命令启动时
  会拉起一个**看护进程**（`dtool __reap --workspace <ws> --pid <父 pid>`）：它只轮询父进程是否还在，
  父进程一消失就调一次 `Recorder.Sync`，把遗留的 `running` 落盘为 `stale`（`error.code=5`），
  因此**无需再跑任何命令**状态就会自愈（实测 ~0.25s）。进程内的任何补偿代码在强杀时都来不及跑，
  这是必须另起进程的原因。
  - 成本：一次 `exec` 自身 + 每 250ms 一次 `pidAlive`；最多活 24h，父进程正常结束就立刻退出。
  - 开关：`DTOOL_NO_REAPER=1`（受限沙箱不允许起子进程）、或 `--no-record`（没有 Action 要收敛）。
  - 局限：整组被杀（`TerminateJobObject`）、机器断电、或用户手动删掉看护进程时，仍退回
    「下一条命令入口 / `actions sync` 收敛为 `stale`」这条兜底路径。

### 4.5 Action 索引

`.dtool/index.json` 保存所有 Action 的摘要列表，避免 AI 逐个读取文件：

```json
{
  "workspace_id": "ws-01HX...",
  "created_at": "2026-10-08T14:00:00Z",
  "actions": [
    {
      "id": "01HX...A",
      "type": "convert",
      "status": "success",
      "started_at": "2026-10-08T14:20:00Z",
      "summary": "转换 data.xlsx → 1024 条记录"
    },
    {
      "id": "01HX...B",
      "type": "query",
      "status": "success",
      "started_at": "2026-10-08T14:23:00Z",
      "summary": "查询返回 2 行"
    }
  ]
}
```

每次 Action 完成或追加注释时原子化更新索引。

### 4.6 Action 作为对话协作载体

这是本工具与普通 CLI 日志的核心区别：**Action 不是只读的审计日志，而是可追加、可派生、可注释的协作对象**。

三种协作模式：

**① 注释（Annotate）**——不改变结果，只追加说明。

```bash
dtool actions annotate 01HX...B \
  --text "这个结果后面还要按季度拆分" \
  --by user
```

AI 在对话中理解用户意图后，调用此命令把上下文写入 Action，后续任何 AI 读取该 Action 都能看到这条注释。

**② 派生（Derive）**——基于某个 Action 的结果，生成新的 Action。

```bash
dtool query \
  --from action:01HX...B \
  --sql "SELECT region, quarter, SUM(total) FROM ..." \
  --notes "按季度拆分上次的汇总"
```

新 Action 的 `derived_from` 指向源 Action，形成派生链。AI 可通过 `actions trace` 看到完整演进。

**`--from` 与数据源解耦**：`--from` 仅表示**血缘（provenance）**，不改变 SQL 读取的数据。SQL 的数据源通过 `--source <别名>=<引用>` 显式绑定（引用语法见 8.2），可重复：

```bash
dtool query --from action:01HX...B \
  --source sales=latest:convert --source prev=action:01HX...B \
  --sql "SELECT region, SUM(amount) FROM sales GROUP BY region"
```

别名对应的文件在执行前被载入内存库，表名即别名。未指定 `--source` 时，SQL 里写文件路径则按相对**工作区产物**解析，找不到再回退到当前目录。这解决了“SQL 中 `data.json` 实际位于 `.dtool/datasets/<name>/` 下”的歧义。

**③ 复用（Reuse）**——直接引用历史 Action 的产物，不重新执行上游。

```bash
dtool visualize --input latest:query --type pie --x region --y total
```

`latest:query` 解析为最近一次成功的 query Action 输出。

---

## 5. CLI 命令设计

工具名 `dtool`。所有命令通用参数：

| 参数 | 说明 |
|------|------|
| `--workspace` | 工作区目录，默认 `./.dtool` |
| `--tags` | 逗号分隔的标签，写入 Action metadata |
| `--notes` | 备注，写入 Action metadata |
| `--from` | 指定派生来源，如 `action:<id>` 或 `latest:<type>` |
| `--preview-rows` | Action 预览行数，默认 20 |
| `--no-record` | 跳过 Action 记录（用于临时查询）；此时 stdout 不含 `action_id`，且不可被后续命令引用 |
| `--sandbox` | 默认开启：SQL 仅允许读取 `--source` 绑定文件与工作区内文件，禁止读取任意系统路径（如 `/etc/passwd`）、URL 与写入类语句 |
| `--load-mode` | JSON 装入方式：`auto`（默认，按内存预算在 full+内存库 / stream+内存库 / stream+磁盘库 三档间选；整块解析仅在输入 <32MB 且预计峰值 ≤ 预算 80% 时使用）/ `stream` / `full` |
| `--max-memory` | 内存预算，如 `4G`/`512M`；`0` 关闭检查（默认自动探测 cgroup v2/v1 与系统可用内存），见 8.4.1 |
| `--store` | SQLite 库落在哪：`auto`（默认，按预算在内存库 / 磁盘库间选）/ `memory`（快）/ `disk`（峰值最低，临时表落盘，见 8.5） |
| `--mem-policy` | 所有档都预计超预算时：`try`（默认）仍试最省档并把失败记入 Action（AI 可读后换招）/ `strict` 直接失败 |
| `-c, --config` | 配置文件，命令行参数优先；严格模式拒绝未知键 |

**退出码与 stdout 约定**：成功退出码 0，stdout 为结构化 JSON；失败退出码非 0
（1 通用、2 参数错误、3 引用不存在、4 执行失败、**5 被中断**），**stdout 仍输出 `ErrorResponse` JSON**，
人类可读日志只写 stderr。

`4` 与 `5` 的区别是语义而非严重程度：`4` = 跑完了但结果不可用（改输入/参数），
`5` = 半路没了、结果未知（重跑即可）；强杀后收敛出来的 `stale` 也标 `5`。

### 5.1 `convert`：Excel → JSON + JSON Schema

```bash
dtool convert \
  --input data.xlsx \
  --sheet "Sheet1" \
  --tags "sales,2026q4"
```

| 参数 | 必填 | 说明 |
|------|------|------|
| `--input` | 是 | Excel 文件路径 |
| `--sheet` | 否 | 工作表名称，默认第一个 |
| `--name` | 否 | 数据集名，缺省取文件名；之后用 `dataset:<name>` 或直接在 SQL 中当表名 |
| `--tags` | 否 | Action 标签 |

**输出**：写入数据集 `.dtool/datasets/<name>/data.json`（及 `data.schema.json`、`dataset.json`），同时生成 Action 记录；同名数据集被覆盖并刷新更新日期。Schema 总会生成。

**转换规则**（避免静默数据损坏）：

- 使用 excelize 的流式 `Rows()` 迭代器，不一次性 `GetRows` 载入全部内存。
- 表头为空或重复时自动改名（`col_3`、`name_2`）并写入 Action 的 `warnings`；保证 JSON key 唯一。
- 短行缺失的单元格填 `null`，而不是省略 key（否则各行字段不一致，SQL 列推断会出错）。
- 完全空行跳过；合并单元格仅首格有值，其余为 `null`，在 `warnings` 提示。
- 类型推断**只对整列统一判断**，不逐单元格转换：以 `0` 开头的数字串（如 `00123`、手机号、身份证）、超过 15 位的整数保持 `string`，避免丢前导零/精度。
- 日期单元格按 Excel 原始序列值转换为 ISO 8601（`2006-01-02` 或带时间），不依赖显示格式。
- `--sheet` 不存在时报错并列出可用工作表；仅支持 `.xlsx`。

stdout 返回结构化结果：

```json
{
  "success": true,
  "name": "data",
  "updated_at": "2026-10-08T14:20:00Z",
  "data_file": ".dtool/datasets/data/data.json",
  "schema_file": ".dtool/datasets/data/data.schema.json",
  "record_count": 1024,
  "columns": ["id", "name", "amount", "region"],
  "action_id": "01HX...A"
}
```

### 5.2 `query`：SQL 查询 JSON 数据

```bash
dtool query \
  --sql "SELECT region, SUM(amount) AS total FROM 'data.json' GROUP BY region" \
  --format markdown \
  --output result.md
```

| 参数 | 必填 | 说明 |
|------|------|------|
| `--sql` | 是 | SQL 语句，表名使用 JSON 文件路径 |
| `--from` | 否 | 派生来源，用于关联上游 Action |
| `--format` | 否 | 输出格式：`json` / `csv` / `markdown` / `table` / `xlsx`（xlsx 为二进制，必须配合 `--output`；数值写为数字单元格，表头加粗，列宽自适应） |
| `--output` | 否 | 输出文件路径 |

**内部实现**：

- 重写 SQL：别名 / 文件路径（双引号、反引号，或 `FROM`/`JOIN` 后的裸路径）解析为文件，同一文件只载入一次。
- 把用到的文件载入内存 SQLite（`sql.Open("sqlite", ":memory:")`，单连接），再执行查询，直接得到带类型的行（`int64` / `float64` / `string`）。
- 沙箱：仅允许单条 `SELECT`/`WITH`（挡住 `ATTACH`/`PRAGMA`）；解析出的文件必须位于工作区、当前目录，或由 `--source` 显式绑定。
- 根据 `--format` 格式化输出；结果集过大时，完整结果落盘到产物文件，stdout 与 Action 只带预览，并受 `--max-rows`（默认 10000）约束。
- 支持 `context.Context` 超时（`--timeout`，默认 60s）。

**stdout 输出示例（json 格式）**：

```json
{
  "success": true,
  "columns": ["region", "total"],
  "rows": [
    {"region": "North", "total": 15000},
    {"region": "South", "total": 23000}
  ],
  "row_count": 2,
  "action_id": "01HX...B"
}
```

### 5.3 `visualize`：结果 → 图表 / 表格

```bash
dtool visualize \
  --input action:01HX...B \
  --type bar \
  --x region \
  --y total \
  --title "Regional Sales" \
  --output chart.png
```

| 参数 | 必填 | 说明 |
|------|------|------|
| `--input` | 是 | 数据来源，支持文件路径、`action:<id>`、`latest:query` |
| `--type` | 是 | 图表类型：`bar` / `line` / `pie` / `table` |
| `--x` | 是* | X 轴字段（表格类型可省略） |
| `--y` | 是* | Y 轴字段（需为数值列，校验失败时报错并列出可用的数值列） |
| `--title` | 否 | 图表标题 |
| `--output` | 否 | 输出文件，默认写入 `.dtool/outputs/charts/<action-id>/` |
| `--format` | 否 | 图表：`png` / `svg`；`--type table` 时可选 `md`（默认）/ `xlsx` |

### 5.4 `pipeline`：组合命令

```bash
dtool pipeline \
  --excel data.xlsx \
  --sql "SELECT region, SUM(amount) AS total FROM 'data.json' GROUP BY region" \
  --chart bar --x region --y total \
  --tags "monthly-report"
```

Pipeline 的各步骤**均为可选**：`--excel` 只做转换（数据持久化为独立的数据集 `datasets/<name>/`，可反复使用）；`--input <ref>` 复用已有数据而跳过转换；省略 `--sql` 则不查询，`--chart` 必须配合 `--sql`。SQL 中用 `data` 引用上游数据。执行的步骤生成子 Action，与父 Action 通过 `parent_id` 关联。

- 子步骤失败时，父 Action 置为 `failed`，`error.detail` 指明失败的子 Action ID；已成功的子 Action 保留，AI 可从失败点用 `--from` 续写，无需重跑。
- 子步骤之间通过 Action 引用传递数据（内部等价于 `latest:` 的精确 ID 绑定），不在内存中隐式共享，保证每个子 Action 可单独复现。

### 5.5 `actions`：查询、追踪、补充 Action

这是 AI 追踪进度、用户对话补充的核心命令组。

```bash
# 列出所有 Action
dtool actions list

# 列出最近 10 条
dtool actions list --limit 10

# 按类型/状态过滤
dtool actions list --type query --status success

# 查看某个 Action 的完整详情（含预览、注释、派生链）
dtool actions show 01HX7Z8K2M9PQR3STUVWXYZABC

# 查看某个 Action 的输出数据（完整数据，非预览）
dtool actions output 01HX7Z8K2M9PQR3STUVWXYZABC

# 查看某条 Action 的派生/演进链路
dtool actions trace 01HX7Z8K2M9PQR3STUVWXYZABC

# 给某个 Action 追加注释（对话式补充）
dtool actions annotate 01HX7Z8K2M9PQR3STUVWXYZABC \
  --text "这个结果后面还要按季度拆分" \
  --by user

# 从 actions/ 重建 index.json
dtool actions reindex

# 显式收敛状态：进程已消失的 running -> stale（落盘并附原因），仍活着的列在 running 里
dtool actions sync

# 导出所有 Action 为单个 JSON（供 AI 一次性读取；支持 --limit / --since 控制体积）
dtool actions export --output .dtool/actions_dump.json
```

**`actions list` 的 stdout（JSON 格式）**：

```json
{
  "actions": [
    {
      "id": "01HX...A",
      "type": "convert",
      "status": "success",
      "started_at": "2026-10-08T14:20:00Z",
      "summary": "转换 data.xlsx → 1024 条记录"
    }
  ],
  "total": 3
}
```

**`actions trace` 的输出**：按时间顺序返回从指定 Action 开始的所有下游 Action（含 `derived_from` 派生链），形成完整的演进路径。

**`actions show` 的输出**：完整 Action JSON，包括预览数据、注释、派生关系，AI 读取后即可回答"这一步做了什么、结果如何、后续可以怎么补"。

**`actions sync` 的输出**：

```json
{"scanned": 2, "stale": 1, "stale_ids": ["01J..."], "running": [{"id": "01K...", "type": "query", "pid": 3453902}]}
```

每条命令启动时也会自动做一次同样的收敛（`openWorkspace`），`sync` 是它的显式出口：
既能立刻拿一份「谁死了、谁还在跑」的快照，也便于在脚本里断言。为让 `sync` 真有活干，
它自己走 `openWorkspaceRaw`（不预先回收）。

---

### 5.6 `version` / `upgrade`：版本与自更新

```bash
dtool version [--short] [--deps]        # 版本与构建元数据；--short 只输出版本号，--deps 附带依赖模块版本
dtool upgrade [--version V] [--channel stable|preview|dev] [--pre] [--skills[=路径]]  # 升级自身，可降级；顺带另存该版本的 SKILL.md
dtool query --sql ... --update          # 查询前先检查更新（内置同一套逻辑）
```

渠道：`stable`（默认，最新正式版）/ `preview`（正式版 + 预览版，等价于旧的 `--pre`）/
`dev`（`main` 的最新构建，滚动发布到 tag `dev`，比的是构建号而不是版本号大小）。
`--version` 可指定任意历史版本（含降级）；dev 构建写 `--version dev` 或 `--version dev-<run id>`，
但 dev 渠道只保留最新一次构建。网络瞬断按指数退避重试 3 次，失败时 `hint` 给出 Releases 页面，见 8.4.3。

`--skills[=路径]` 把**目标版本**的 `SKILL.md`（Agent 技能手册）另存一份：目录（不存在则创建）写成
`<目录>/SKILL.md`，明确以 `.md` 结尾的路径当文件，裸 `--skills` 写当前目录。手册直接从已经过
sha256 校验的平台归档里取（不额外走网络），因此永远与刚装上的二进制同源；已经是最新版本时也可以
单独取（只下载校验归档，不碰二进制）。结果里的 `skills_changed` 表示内容是否变化。

## 6. JSON Schema 生成设计

### 6.1 定位

Schema 是 `convert` 子命令的**副产品**，与 JSON 数据一起产出：

```
Excel ──► [convert] ──┬──► data.json
                      └──► data.schema.json   ← 从数据推断，而非从 Go struct 反射
```

核心目的是**让 AI 快速理解数据结构**，因此输出应精简、可读、字段语义清晰。

### 6.2 输出格式

```json
{
  "source": "data.xlsx",
  "sheet": "Sheet1",
  "record_count": 1024,
  "updated_at": "2026-10-08T14:20:00Z",
  "columns": [
    {
      "name": "id",
      "type": "integer",
      "nullable": false,
      "unique": true,
      "samples": [1, 2, 3]
    },
    {
      "name": "region",
      "type": "string",
      "nullable": false,
      "enum": ["North", "South", "East", "West"],
      "samples": ["North", "South"]
    },
    {
      "name": "amount",
      "type": "number",
      "nullable": true,
      "min": 0,
      "max": 99999.99,
      "samples": [1500.00, 2300.50]
    },
    {
      "name": "created_at",
      "type": "date",
      "format": "2006-01-02",
      "samples": ["2026-01-15", "2026-02-03"]
    }
  ]
}
```

这种格式对 AI 特别友好：

- **`type`** 直接告诉 AI 字段的 SQL 对应类型（`integer` → `INTEGER`，`date` → `DATE`）。
- **`enum`** 让 AI 知道某字段的取值空间，便于生成 `WHERE region IN (...)` 这类查询。
- **`samples`** 给 AI 提供具体值参考，减少猜测。
- **`nullable`** 提示 AI 是否需要处理 NULL。
- **`min`/`max`** 帮助 AI 判断数值范围，避免生成越界条件。

### 6.3 推断逻辑

```go
type ColumnSchema struct {
    Name     string   `json:"name"`
    Type     string   `json:"type"`      // integer / number / boolean / date / string
    Nullable bool     `json:"nullable"`
    Unique   bool     `json:"unique,omitempty"`
    Enum     []string `json:"enum,omitempty"`
    Min      *float64 `json:"min,omitempty"`
    Max      *float64 `json:"max,omitempty"`
    Format   string   `json:"format,omitempty"` // 日期格式
    Samples  []any    `json:"samples,omitempty"`
}

func InferSchema(records []map[string]any, headers []string) []ColumnSchema {
    // 对每列：
    // 1. 收集所有非空值
    // 2. 依次尝试解析为 int64 / float64 / bool / time.Time
    // 3. 若全部成功，则确定类型；否则降级为 string
    // 4. 统计 nullable、unique、min/max
    // 5. 若唯一值数量 <= 阈值（如 20），输出 enum
    // 6. 取前 3 个样本值
}
```

**判断规则**：

| 场景 | 处理 |
|------|------|
| 列中所有值可解析为整数 | `integer` |
| 列中有小数但可解析为浮点 | `number` |
| 列中只有 `true`/`false` | `boolean` |
| 列中所有值匹配日期格式 | `date`（附 `format`） |
| 混合类型或无法解析 | `string` |
| 存在空值 | `nullable: true` |
| 唯一值数 ≤ 20 且为字符串 | 输出 `enum` |
| 唯一值数等于记录数 | `unique: true` |
| 以 `0` 开头的数字串 / 超 15 位整数 | 保持 `string`（防止丢失前导零与精度） |
| 整列为空 | `type: "null"`，`nullable: true` |
| 大数据量 | 单遍流式统计；`unique`/`enum` 的去重集合超过上限（如 10 万）即放弃并省略 |

`date` 在 SQLite 中实际以 TEXT 存储（ISO 8601），比较与排序依赖字符串序；Schema 中为 `date` 列附 `sql_type: "TEXT"` 提示 AI 使用 `date()` 等函数。

---

## 7. AI 交互场景

### 7.1 AI 询问进度

```
用户: "现在的进度怎么样了？"
AI:  执行 dtool actions list --limit 5
     读取结果，回答: "已完成 Excel 转换（1024 条记录）和 SQL 查询（2 行结果），
                    目前正在生成柱状图..."
```

### 7.2 AI 诊断失败

```
用户: "为什么图表没生成？"
AI:  执行 dtool actions list --type visualize --status failed
     找到失败的 Action，读取 error 字段
     回答: "字段 'amount' 在查询结果中不存在，实际字段为 'total'"
```

### 7.3 AI 复用结果

```
用户: "用上次查询的结果画个饼图"
AI:  执行 dtool actions list --type query --limit 1
     获取 Action ID: 01HX...B
     执行 dtool visualize --input action:01HX...B --type pie --x region --y total
```

### 7.4 AI 读取 Schema 后生成 SQL

```
AI:  执行 dtool datasets show sales，读取其中的 schema
     得知 region 是 string 且有 enum，amount 是 number
     生成: SELECT region, SUM(amount) AS total
           FROM 'data.json'
           WHERE region IN ('North', 'South')
           GROUP BY region
```

### 7.5 用户对话式补充（核心场景）

**场景：基于已有结果追加分析**

```
用户: "上次那个按地区的汇总，再按季度拆一下"
AI:  1. 执行 dtool actions list --type query --limit 1
        获取 Action 01HX...B，读取其 output.columns 和 preview
     2. 执行 dtool query \
          --from action:01HX...B \
          --sql "SELECT region, quarter, SUM(total) AS total
                 FROM 'data.json'
                 GROUP BY region, quarter" \
          --notes "按季度拆分上次的汇总"
     3. 新 Action 的 derived_from 指向 01HX...B
     4. 回答: "已按季度拆分，结果如下..."
```

**场景：给某一步加备注，供后续 AI 参考**

```
用户: "标记一下，这个查询结果的口径是含税的"
AI:  执行 dtool actions annotate 01HX...B \
       --text "口径：含税" --by user
     回答: "已记录，后续读取该 Action 时会看到这条注释。"
```

**场景：基于注释续写**

```
用户: "把之前标记含税的那个结果画成图"
AI:  1. 执行 dtool actions list，找到带注释的 Action
     2. 读取 annotations 确认是 01HX...B
     3. 执行 dtool visualize --input action:01HX...B --type bar --x region --y total
```

### 7.6 AI 一次性获取全部上下文

```
AI:  执行 dtool actions export
     读取 .dtool/actions_dump.json
     在单次请求中理解完整 Pipeline 状态、注释和派生链
```

---

## 8. 模块详细设计

### 8.1 Action Recorder

**职责**：为每次命令执行创建、更新、索引 Action 记录，并支持注释追加。

```go
type Action struct {
    ID           string                 `json:"id"`
    Type         string                 `json:"type"`
    Status       string                 `json:"status"`
    StartedAt    time.Time              `json:"started_at"`
    FinishedAt   *time.Time             `json:"finished_at,omitempty"`
    DurationMs   int64                  `json:"duration_ms,omitempty"`
    Command      string                 `json:"command"`
    Input        map[string]any         `json:"input"`
    Output       *ActionOutput          `json:"output,omitempty"`
    ParentID     string                 `json:"parent_id,omitempty"`
    DerivedFrom  string                 `json:"derived_from,omitempty"`
    Annotations  []Annotation           `json:"annotations,omitempty"`
    Error        *ActionError           `json:"error,omitempty"`
    Metadata     ActionMetadata         `json:"metadata"`
}

type Annotation struct {
    At   time.Time `json:"at"`
    By   string    `json:"by"`   // "user" / "ai-agent"
    Text string    `json:"text"`
}

type Recorder struct {
    workspace string
}

func (r *Recorder) Start(actionType string, input map[string]any) (*Action, error)
func (r *Recorder) Success(a *Action, output *ActionOutput) error
func (r *Recorder) Fail(a *Action, err error) error
func (r *Recorder) Annotate(id, text, by string) error
```

**实现要点**：

- Action 文件使用 ULID 命名，天然按时间排序。
- 写入采用**先写临时文件，再 rename** 的原子操作，避免 AI 读到半截文件。
- 注释追加时读取原文件、追加 `annotations`、原子化写回。
- 索引文件在每次 Action 完成或注释追加时同步更新。

### 8.2 数据源引用语法

为了让 Action 之间能自然衔接，所有 `--input` / `--from` / `--source` 参数支持以下形式（`--from` 仅记录血缘，见 5.2）：

> 安全：`action:<id>` 的 ID 必须匹配 ULID 格式再拼路径，防止 `../` 路径穿越；`latest:<type>` 只取 `status=success` 的 Action，且 `stale/running` 不参与。

| 形式 | 含义 |
|------|------|
| `path/to/file.json` | 直接文件路径 |
| `action:<id>` | 引用某 Action 的主输出 |
| `dataset:<name>` | 引用数据集（`datasets/<name>/data.json`） |
| `latest:<type>` | 引用最近一次成功的某类型 Action 输出 |

### 8.3 Excel 转换模块

**流式两阶段、单次解析**（峰值内存 ≈ 32MB + 文件 × 3，与行数无关）：

```go
func ConvertExcel(o Options) (*Result, error) {
    // 阶段 1：逐行读单元格 → 每列累积统计（colStat）+ 原始值落临时 JSONL
    f, _ := excelize.OpenFile(o.Input)
    it, _ := f.Rows(o.Sheet)          // 迭代器是流式的；GetMergeCells/GetRows 会整表物化
    for it.Next() {
        cells, _ := it.Columns()
        for i := range headers { stats[i].observe(cells[i]) }
        jsonl.Write(cells)            // 一行一个 JSON 数组，保持列序
    }
    f.Close()                          // 释放 excelize 的大表临时文件

    // 阶段 2：按 stats 推断出的 Schema 读回 JSONL，逐行写成 data.json
    schema := statsPerColumn(stats, records)   // 与 InferSchema 同一实现
    for line := range jsonlLines {             // 只驻留一行
        rowWriter.Write(typedRow(line, schema)) // 缩进与 json.MarshalIndent 逐字节一致
    }
}
```

要点：

- 单元格读取只用 `File.Rows()` 迭代器。`GetMergeCells`/`GetRows`/`GetCols` 都会走
  `workSheetReader`，把整张表反序列化成 `xlsxWorksheet` 缓存到 `File` 里——实测
  15 万行 × 9 列要多花 855MB。合并单元格数量改为直接扫 zip 里的 worksheet XML
  （逐 token、内存 O(1)，见 `internal/converter/merge.go`）。
- 类型推断不能只看采样，必须整列：阶段 1 的 `colStat` 累积「是否全部满足整数/数字/
  布尔/日期」、非空数、去重数（≤10 万）、首个不同值（用于 enum 与样例）、min/max。
  `InferSchema` 也改为用同一个 `colStat`，保证「整列推断」只有一份实现。
- `data.json` 由 `rowWriter` 逐行写出，缩进与 `json.MarshalIndent([]types.Row, "", "  ")`
  完全一致（有测试逐字节比对），因此产物对下游没有格式变化。
- 调用方不再拿到全部记录：`Result` 只带 `RecordCount` 与 `Preview`（供 Action 预览）。
- 预检倍率随之从「×260」换成线性模型「32MB + 文件 × 6」（实测 ×3，留 1.7 倍余量），
  见 8.4.1。

### 8.4 SQL 查询模块

```go
// internal/query
func Run(ctx context.Context, o Options) (*types.QueryResult, error) {
    sqlText, binds, err := Rewrite(o)         // 沙箱校验 + 表名改写
    db, _ := sql.Open("sqlite", ":memory:")   // modernc.org/sqlite
    for _, b := range binds { loadTable(ctx, db, b) } // 整列类型推断后建表、插入
    return collect(ctx, db, sqlText, o.MaxRows)
}
```

**表名约定**：优先使用 `--source` 别名；也可写文件路径，例如 `SELECT * FROM "data.json"`（双引号包裹）。

### 8.4.1 内存预算与 OOM 防护（internal/memguard）

载入是「把整个 JSON 解析进内存 SQLite」，峰值内存与输入体积成正比（实测 JSON ≈ ×13）。这带来一个严重体验问题：**内存不足时进程会被内核 OOM 直接杀掉，用户看不到任何原因**。memguard 把这种情况变成普通错误：

| 环节 | 行为 |
|------|------|
| 载入前 | `CheckSize` 按输入体积 × 倍率估算峰值，超出可用预算即失败，`detail` 给出体积、倍率、可用量与来源，`hint` 给出退出口 |
| 运行期 | `Watch` 每 200ms 采样本进程内存（Linux 读 `/proc/self/status`，其他平台用 Go 统计），超过预算即 `context.CancelCause`；`PressureError` 把它转成用户可读错误 |
| 兜底 | 数据源 ≥8MB 时打印载入进度到 stderr；即便被强杀，用户也能看出卡在哪个文件 |
| 软上限 | 同时调用 `debug.SetMemoryLimit(预算)`，让 GC 提前发力，尽量不碰 cgroup 硬限制 |

预算来源：`--max-memory` > `DTOOL_MAX_MEMORY` > cgroup v2/v1 限制（`min(limit-used, MemAvailable)`）> 未知（不检查）。统一预留 15% 余量，其中 Go 堆软上限再降一档到可用预算的 3/4。失败也会写入一条 `failed` 的 Action，便于事后追溯。

估算模型都来自实测：JSON 整块解析 ≈ 文件 × 13、流式装入 ≈ × 2；xlsx 转换是「32MB 固定开销 + 文件 × 3」的线性模型（见 8.1）。倍率不同，**处置建议也必须分场景**：`--load-mode` 只对 `query` 成立；`convert` 只有一个流式实现、没有开关可切（峰值只随文件体积增长）。它作为全局参数会被 cobra 接受、但没人读，等于静静忽略——所以 CLI 用 `warnInertLoadMode` 在 stderr 补一句「已忽略」，别让用户以为换了参数就会变。同理 `CheckSize`/`CheckNeed` 显式接收 `hint`（`HintLoadMode` / `HintSplitInput`），并有测试锁定这一点。

1 核 2GB 实测：7.4MB / 15 万行 × 9 列的 xlsx 现在峰值 47MB、12.8s（改造前 1.9GB、21.2s）；17.8MB / 40 万行的 xlsx 现在 74MB、23.5s（改造前在 2GB 上限下直接 OOM）。预算不足（如 `--max-memory 100M`）时仍在转换前拦下并给出「32MB 固定开销 + 文件 × 6 的流式估算需 134MB」。


**Windows 与 Job Object**（Bug3 的教训）：平台探测必须**以 `QueryInformationJobObject` 为准**，
`IsProcessInJob` 只作佐证。后者把「不在任何 Job 里」和「调用失败」都表示成 0，一旦把它当闸门，
上限探测就被静默短路——实测（1 核 / 256MB 上限）：预检按系统可用内存 16.9GB 放行 118MB 的输入，
5 轮里 2 轮 Go runtime `fatal error: out of memory`（`VirtualAlloc` 返回 1455 = 提交量耗尽，
**不可恢复**，连 recover 都没机会）、2 轮优雅失败、1 轮挂起。修正后同样输入在预算 174MB 处
被预检拒绝（code 4，`detail` 写明「来源=Windows Job Object 进程内存上限」）。
两种标志都认：`0x100` 限单进程提交量，`0x2000` 限整个 Job 的提交量，同时设置取更小值；
用量口径是**私有提交量**（`PagefileUsage`）而不是工作集。
`meminfo` 的 `job_object` 保留全部原始字段（两次调用的返回值与 `last_error`、标志、两个上限），
读取失败时 `warnings` 直接给出「预算回落到系统可用内存，请用 `--max-memory`」——
这类 bug 的代价全在「读不到上限却装作读到了」，所以证据必须能自证。

**软上限为什么是 3/4 而不是 100%**：`debug.SetMemoryLimit` 只约束 Go 堆，而
modernc/SQLite 的页缓存是 mmap/VirtualAlloc 出来的、runtime 元数据也在堆外，
这部分同样计入 cgroup 与 Job Object 的提交量。堆按满预算走，加上堆外开销正好把提交顶到硬上限。

#### 8.4.2 装入方式（--load-mode）

| 方式 | 实现 | 峰值内存 | 说明 |
|------|------|----------|------|
| `full` | 整块 `Unmarshal` 成 `[]Row` 再插入 | ≈ 文件 × 13 | 快，但内存线性放大 |
| `stream` | 两遍流式：第一遍按 JSON 键序建立列与类型，第二遍逐行插入 | ≈ 文件 × 2 | 内存与文件大小基本无关，代价是解析两遍 |

`auto`（默认）按**实际文件大小**自适应：`size ≥ 32MB` 即转流式；此外若 `size × 13` 超出可用预算也转流式。流式用 `json.Decoder` 逐 token 读取，**保持行内键序**（map 会丢顺序，列顺序必须稳定），并逐行累积类型统计（布尔列与整块解析一致落成 INTEGER 0/1）。两种方式对同一输入产生完全相同的列顺序、类型与结果（有等价性测试覆盖）。

`--timeout` 只约束查询阶段；载入是本地的读写与 CPU 工作，大文件可能远超默认 60s，把它算进去会让默认值变成陷阱（载入由内存看门狗与信号中断兜底）。

**回归测试**（测试环境、方式与完整结果见 [docs/PERFORMANCE.md](docs/PERFORMANCE.md)）：结论由测试而不是文档守住。`internal/query/perf_test.go` 的 `TestPerfLoadModeMemoryRatio` 断言流式存活堆峰值比整块解析低 1.3 倍以上（实测 10–20 倍；采样方式见下——直接读 HeapAlloc 会把未回收的垃圾算进来，在 CI 上曾把差距压到 1.28 倍而误报），`internal/pipeline/perf_test.go` 的 `TestPerfConvertExcelMemory` 断言转换峰值不超过 `xlsxPeakFactor × 1.3`（HeapAlloc 口径低于 CLI 的 RSS，留 30% 余量，用来拦量级回归）。峰值采样放在只被 `_test.go` 引用的 `internal/perftest`，两种口径并列：`Inclusive`（直接读 HeapAlloc，含垃圾，接近 RSS，适合上界断言）与 `Live`（每次采样先 GC 取存活集，适合比较「谁更省内存」）；吞吐走 `Benchmark*`（`make bench`，`DTOOL_BENCH_ROWS` 放大）。这些随默认的 `go test ./...` 运行（`-short` 跳过），CI 另跑一轮 `-benchtime 1x` 的基准冒烟。

### 8.5 图表生成模块

```go
import charts "github.com/vicanso/go-charts/v2"

func RenderChart(data *QueryResult, chartType, xField, yField, title, output string) error {
    switch chartType {
    case "bar":
        chart, err := charts.BarRender(
            map[string][]string{xField: extractStrings(data, xField)},
            charts.WithTitleOpts(charts.TitleOption{Title: title}),
        )
        f, _ := os.Create(output)
        return chart.Render(f)
    case "line":
        // 类似
    case "pie":
        // 类似
    }
    return nil
}
```


**中文字体**：go-charts 内置字体不含中文。字体按以下优先级解析（`internal/visualize/font.go`）：

1. `--font` 命令行参数
2. `-c config.yaml` 中的 `font`
#### 8.4.3 执行档阶梯与选档（internal/memguard/plan.go）

内存上限的作用是**选执行档**，不是「过 / 不过」。档位把两个维度合成：装入方式
（`full` / `stream`）× 库位置（内存 / 磁盘）：

| 档 | 实测峰值/输入（123MB 输入，1 核） | 用于估算的上界 |
|---|---|---|
| `full` + 内存库 | 7.75× | 13× + 32MB |
| `stream` + 内存库 | 1.44×（≤2.44×） | 2× + 32MB |
| `stream` + 磁盘库 | **0.16×**（20MB） | 0.3× + 24MB |

磁盘档是「不拦截」的关键：页缓存变成文件页后可被系统回收、也不计入进程私有提交，
所以同一份 123MB 输入在 256MB 硬上限下从「预检拒绝」变成「跑完，14.5s，峰值 51MB」。
没有 `full` + 磁盘档——它的峰值来自 Go 堆，换库不省（显式组合会报用法错误）。

**选档函数**（纯函数，Linux 可测）：

```
Choose{Size, Memory, Candidates, Forced, Policy, Samples} -> Plan{Chosen, Threshold, Chance, Reason, Rejected}
  threshold = Memory.Threshold()        // 软预算=可用量；硬上限（cgroup/Job）再打八折
  for 档 in 快到省:
      pred = Base + Size×Factor
      if pred ≤ threshold           -> 选它（放得下，不必浪费一次尝试）
      if TryChance(样本, 档, pred, threshold) ≥ MinChance(0.5) -> 选它（擦边，值得一试）
      else 记入 Rejected（附原因）并继续
  都不行 -> Policy=try：仍选最省档并标 Risky（失败写进 Action，AI 据此换档）；
            Policy=strict：交给调用方失败
```

**擦边概率**按工作区已有的执行记录算（这就是「按已执行过的做数学计算」）：

```
Chance = P(ratio ≤ threshold / pred), ratio = 实测峰值 / 预计峰值
Chance = (n·经验CDF + 先验权重3·正态CDF(ratio~N(0.85, 0.30))) / (n + 3)
```

样本 < 5 条时并入其他档的 ratio（偏差形态共通），> 200 条按时间裁剪。先验保证
「没有历史时偏保守」而不是偏乐观。

**(b) 估算按历史校准**：倍率只是上界，平台差异极大（同一份 12MB 输入：Linux 内存档
1.44×，Windows 沙箱实测 ≈15×）。于是每次执行后把「预计 vs 实测」写进历史，选档时按
**同档** ratio 的 P90 上修估算（8 倍封顶，只上修不下修）：没有这一步，判「放得下」的
档会被反复选中、反复撞看门狗，试错永不收敛；有了它，同一个输入第二次就换到能过的档。
`meminfo` 的 `plan.rungs[]` 同时给出 `raw_need`（倍率直算）与 `need`（校准后）。

**(c) 上限读不到时不装作知道**：Windows 上 Job Object 可能「标志位设了、值读回 0」
（`job_object.limit_unreadable`）。此时预算只是本机空闲内存，`Memory.Uncertain=true`：
选档排除整块解析档（峰值比输入大一个数量级），stderr 与 `meminfo.warnings` 都明确说
「预算不可信，请用 --max-memory 指定」。操作者显式 `--load-mode full` 时不干预。

**记录**（`.dtool/plans/samples.json`，`--no-record` 时不写）：每次执行写一条
`{rung, size, predicted, peak, ok, ms, source, at}`，保存时重算派生统计（成功率、
ratio 的 P10/P50/P90、耗时中位数）。fatal 崩溃时进程什么都不剩，只有这份记录还在——
它是「同一个输入只付一次试错代价」的载体。

**升级渠道（8.8.1）**：`stable` / `preview` / `dev` 三选一，`--channel` 指定（`--pre` 是
`preview` 的旧写法）。前两个渠道比版本号（`vX.Y.Z` / `-preview.N`；**按语义化版本比较，不依赖 GitHub 列表顺序**——那张列表按 tag 名字典序排，`preview.10` 会排在 `preview.9` 下面，所以预览序号也**不能零填充**：`preview.010` 不是合法 semver，会被解析器跳过而变得不可见），`dev` 渠道比**构建身份**：
dev 构建之间没有版本序，硬比大小会出现「装完又说有新版」或「悄悄降级」。

为此 dev 产物必须发成 **Release 资产**（updater 走 Releases API，读不到 workflow artifact），
且用**滚动发布**：固定 tag `dev`，每次 main 构建把同名资产 `--clobber` 覆盖，
构建号写在 `dev-build.txt` 第一行（权威），发布标题同步为 `dev-<run id>`。
资产名固定（`dtool_dev_<os>_<arch>.<ext>`）与「构建号必须能区分构建」是一对矛盾：
前者为了覆盖上传，后者交给 `dev-build.txt`——改名后必须**重算 checksums.txt**
（升级端按资产名查哈希），这段逻辑落在 `scripts/dev-release-assets.sh` 并有单测。

**触发与去重**（`.github/workflows/dev-release.yml`）：只有每 4 小时定时与手动触发（`force` 可强制）
两种——**刻意不挂 push**：每次提交都跑一轮六平台构建 + 覆盖上传，既烧 CI 又让下面的 commit 校验
必然失效（push 时 commit 总是新的）。想「推完立刻要 dev」就手动触发。两种触发都先跑
`scripts/dev-release-decision.sh`：把 `dev-build.txt` 里的 commit
与当前 commit 比对，相同就跳过构建与上传——定时触发十有八九命中这条，省掉无意义的构建与
构建号滚动，也让「dev 构建号」只在 main 真的前进时才变。**滚动 tag 的卫生问题**（容易踩）：`dev` 是**移动 tag**，每次都落在 main 最新提交上，而
`git describe` 挑「最近的 tag」——不排除它就会污染两处：本地构建版本变成 `dev` 或
`dev-1-g<sha>`（后者会被 `buildinfo.Channel` 认定成 dev 渠道构建），发版说明的对比基线退化成
移动的 `dev`（内容无意义、不可复现）。因此 `scripts/version.sh`（Makefile 的 VERSION 默认值）与
`scripts/release-info.sh`（prev 基线）都显式 `--exclude dev --exclude 'dev-*'`，
`scripts/tests/dev-tag-hygiene.test.sh` 用「dev 比版本 tag 更近」的恶意布局 + 清理候选集三处断言
把它钉住。预览版清理本来只匹配 `v*-preview.*`，天然安全。

**只保留最新一次**是刻意的：
dev 是「临时构建」，需要长期或可复现的构建请用 preview/stable；要装某次特定构建，
用那一次的 workflow artifact（14 天）。

**操作者可强行指定**：`--load-mode`（`full`/`stream`）与 `--store`（`memory`/`disk`）
两个正交开关；都留 `auto` 时走阶梯。只剩一档时视为强制（理由里写明「操作者指定」），
强制档即使预计会崩也会执行——排查需要，且失败会被完整记录。

**结果与观测**：`QueryResult.strategy` / `strategy_note` 说明用了哪一档、为什么；
`meminfo` 的 `plan` 段列出整条阶梯的预计峰值与历史成功率（`verdict`: ok / borderline /
risky / forced），`ladder` 段给出档位定义。


3. 环境变量 `DTOOL_FONT`
4. 自动发现系统字体：扫描平台字体目录（Linux `/usr/share/fonts` 等、macOS `/System/Library/Fonts` 等、Windows `%WINDIR%\Fonts`），按文件名关键字（Noto Sans CJK、文泉驿、微软雅黑、苹方、黑体…）排优先级，逐个校验可解析，并**优先选同时含汉字与数字字形的字体**（仅 CJK 的字体如 Droid Sans Fallback 只作兜底，并给出警告，否则坐标轴数字会显示为方框）。

支持 `.ttf` 与 `.ttc`（自动抽出第一个字体）；CFF 轮廓的 `.otf`（如 Noto Sans CJK 的 OTF/OTC）渲染库无法解析，会被跳过。仅当标题/标签含非 ASCII 字符时才触发字体解析；显式指定的字体不可用时直接报错，不静默回退。

**配置文件**（`-c config.yaml`，命令行参数优先；相对路径相对配置文件所在目录，支持 `~/`）：

```yaml
font: /usr/share/fonts/truetype/arphic/uming.ttc
workspace: .dtool
preview_rows: 20
load_mode: auto       # auto / stream / full
```

### 8.6 输出格式化模块

| 格式 | 实现 |
|------|------|
| JSON | `encoding/json`，输出 `{columns, rows, row_count}` |
| CSV | `encoding/csv`，首行写列名 |
| Markdown | 手动拼接 `| col |` 表格 |
| Table | 使用 `olekukonko/tablewriter` 或简单对齐 |

### 8.7 Pipeline 编排模块

```go
func RunPipeline(cfg *PipelineConfig) error {
    convResult, err := ConvertExcel(cfg.Excel, "", cfg.OutputDir, true)
    queryResult, err := QueryJSON(cfg.SQL, []string{convResult.DataFile})
    return RenderChart(queryResult, cfg.ChartType, cfg.X, cfg.Y, cfg.Title, cfg.Output)
}
```

---

## 9. 数据流

```
Excel 文件
   │
   ▼
[convert] ──► Action A ──► .dtool/datasets/<name>/data.json + data.schema.json（独立数据集）
   │
   ▼
[query] ──► Action B ──► .dtool/outputs/queries/<id>/result.json
   │                        │
   │                        ├──► [--format markdown] ──► Markdown
   │                        └──► [visualize] ──► Action C ──► chart.png
   │
   ▼
用户对话补充
   │
   ├──► [actions annotate] ──► Action B 追加注释
   └──► [query --from action:B] ──► Action D（derived_from: B）
                                    │
                                    ▼
                              [visualize] ──► Action E ──► 新图表
   │
   ▼
### 8.8 自更新（internal/updater）

`--update` 与 `upgrade` 都走 GitHub Releases API：列 release → 选资产 → 下载 → 校验 `checksums.txt` 的 sha256 → 新二进制自检 → 替换文件。整条链路的失败模式里，**「网络瞬断」和「真的没有新版本」必须区分开**：前者重试就能过去，后者重试只是浪费时间。

| 失败 | 处置 |
|------|------|
| `EOF` / `connection reset` / `broken pipe` / 各种 timeout / TLS handshake 失败 | 可重试：指数退避 500ms → 1s → 2s，上限 10s；最多 3 次尝试 |
| HTTP 5xx、429（含 `Retry-After`） | 可重试（服务端侧问题） |
| HTTP 404（release 不存在）、403（限流，`hint` 提示 `GITHUB_TOKEN`）、非 https、超出体积上限 | 立即失败，不重试 |
| `context.Canceled` / `DeadlineExceeded` | 立即失败，映射为 `CodeInterrupted`（5） |

重试与下载共用同一条 `fetch` 路径（`get` → 状态码分类 → `io.ReadAll` 限长），所以 API JSON 和资产下载的行为一致。`Attempts` / `RetryDelay` 是 `Updater` 的字段，测试把它压到毫秒级，避免为了覆盖退避逻辑而真的睡 3.5 秒。

失败信息分两层：`error` 说明「做了什么、试了几次」（如 `request failed: unexpected EOF（已尝试 3 次）`），`hint` 给出**不依赖本工具**的退出口 —— `https://github.com/dezhishen/dtool/releases`（可用 `--version` 指定版本，`DTOOL_UPDATE_API` 换镜像）。这样即便 GitHub 完全不可达，用户与 AI Agent 都有明确的下一步，而不是反复重跑同一条命令。

---

[actions list / show / trace / export] ──► AI 读取进度、结果、注释、派生链
```

每个环节都产出 Action，AI 可随时通过 `dtool actions` 命令或直接读取 `.dtool/actions/` 目录了解全局，并基于已有 Action 续写新步骤。

---

## 10. 目录结构

```
dtool/
├── main.go                 # 入口，注入 version，调用 cmd.Execute()
├── cmd/
│   ├── root.go
│   ├── convert.go
│   ├── query.go
│   ├── visualize.go
│   ├── pipeline.go
│   └── actions.go
├── internal/
│   ├── action/
│   │   ├── action.go
│   │   ├── recorder.go
│   │   ├── index.go        # 含 reindex
│   │   ├── lock.go         # 工作区文件锁
│   │   ├── annotate.go     # 注释追加
│   │   └── resolver.go     # 处理 action:<id> / latest:<type> 引用
│   ├── workspace/
│   │   └── workspace.go
│   ├── converter/
│   │   ├── excel.go
│   │   └── schema.go       # Schema 推断
│   ├── query/
│   │   ├── query.go        # 重写/沙箱/执行
│   │   ├── load.go         # JSON → 内存 SQLite
│   │   └── perf_test.go    # 装入方式的内存/吞吐回归
│   ├── visualize/
│   │   └── chart.go
│   ├── formatter/
│   │   ├── json.go
│   │   ├── csv.go
│   │   └── markdown.go
│   ├── perftest/
│   │   └── perftest.go     # 峰值堆/分配量采样（仅测试引用）
│   └── pipeline/
│       ├── runner.go
│       └── perf_test.go    # Excel 转换的内存/吞吐回归
├── pkg/types/
│   └── result.go
├── skills/dtool/SKILL.md    # Agent Skill 包（目录名 = frontmatter 的 name）；打包时复制成归档根目录的 SKILL.md
├── go.mod
├── Makefile
└── README.md
```

---

## 11. 关键接口

### 11.1 统一结果结构

```go
type QueryResult struct {
    Columns  []string         `json:"columns"`
    Rows     []map[string]any `json:"rows"`
    RowCount int              `json:"row_count"`
}

type ConvertResult struct {
    DataFile    string   `json:"data_file"`
    SchemaFile  string   `json:"schema_file,omitempty"`
    RecordCount int      `json:"record_count"`
    Columns     []string `json:"columns"`
}
```

### 11.2 Action 输出结构

```go
type ActionOutput struct {
    Files            []string         `json:"files,omitempty"`
    RowCount         int              `json:"row_count,omitempty"`
    Columns          []string         `json:"columns,omitempty"`
    SchemaRef        string           `json:"schema_ref,omitempty"`
    Preview          []map[string]any `json:"preview,omitempty"`
    PreviewTruncated bool             `json:"preview_truncated,omitempty"`
    Summary          string           `json:"summary"`
}
```

### 11.3 错误响应

```go
type ErrorResponse struct {
    Error    string `json:"error"`
    Detail   string `json:"detail"`
    Code     int    `json:"code"`
    ActionID string `json:"action_id,omitempty"`
}
```

---

## 12. 错误处理

- **文件不存在**：返回 JSON 错误，并写入 Action 的 `error` 字段。
- **SQL 语法错误**：返回 SQLite 原始错误 + `hint`，例如 `"hint": "检查表名是否为 JSON 文件路径"`。
- **图表字段缺失**：返回 `field not found`，并在 Action 的 `error.detail` 中列出可用字段。
- **崩溃中断**：Action 停留在 `running`；下次任何命令启动时会回收为 `stale`（Action 文件与索引一起落盘，
  并写明「进程已消失…结果未知」，`error.code=5`），AI 通过 `actions list --status stale` 可发现并重试。
- **信号中断**：Ctrl+C / SIGTERM 走正常错误路径：`status: failed`、`error.code=5`、消息含阶段
  （载入 / 查询 / Excel 转换），`hint` 让你直接重跑。
- **沙箱拒绝**：SQL 访问被禁止的路径时，返回 `sandbox violation` 并说明允许的范围。
- **超时/超限**：`--timeout` 或 `--max-rows` 触发时返回明确错误码，而不是截断后假装成功。
- **引用失效**：`action:<id>` 指向不存在的 Action 时，返回 `action not found` 并列出最近可用的 Action ID。
- **网络瞬断**：自更新（`--update` / `upgrade`）遇到 `EOF`、连接重置、5xx、429 时按指数退避自动重试（3 次）；彻底失败的错误里写明尝试次数，`hint` 给出 Releases 页面，直接手动下载即可。

---

## 13. 构建与部署

### 13.1 构建命令

```bash
CGO_ENABLED=0 go build -o dtool -ldflags="-s -w" .
```

### 13.2 交叉编译

```bash
GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build -o dtool-linux-amd64
GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build -o dtool-darwin-arm64
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o dtool-windows-amd64.exe
```

### 13.3 Makefile

```makefile
BINARY=dtool
VERSION=$(shell git describe --tags --always)

build:
	CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=$(VERSION)" -o $(BINARY) .

test:
	go test ./...

clean:
	rm -f $(BINARY)
```

---

## 14. 扩展方向

- **Action 事件流**：Action 完成或注释追加时可选推送事件到 webhook。
- **多工作区**：通过 `--workspace` 切换不同数据集。
- **配置文件**：`dtool.yaml` 定义常用 Pipeline 模板。
- **REPL 模式**：交互式探索数据与 Action 历史。
- **MCP Server**：内置 MCP server，把 Action 查询、注释、派生暴露为 AI 工具。
- **Action 回放**：根据 Action 记录重放整条 Pipeline。
- **数据校验**：基于生成的 JSON Schema 校验每次转换的数据完整性。

---

## 15. 附录：依赖清单

```
github.com/spf13/cobra
github.com/xuri/excelize/v2
gopkg.in/yaml.v3
modernc.org/sqlite
github.com/vicanso/go-charts/v2
github.com/olekukonko/tablewriter
github.com/oklog/ulid/v2
```

全部为纯 Go 依赖，无 CGO 要求。Schema 推断逻辑自研，不引入外部 Schema 库。

---

## 16. 核心亮点总结

| 特性 | 价值 |
|------|------|
| **Action 自包含** | 每步的输入、产物、预览、Schema 都在记录里，AI 无需重新执行即可理解 |
| **数据预览** | Action 内嵌前 N 行数据，AI 大多数场景无需打开产物文件 |
| **注释追加** | `actions annotate` 让用户通过对话把意图写入 Action，供后续 AI 参考 |
| **派生链** | `--from action:<id>` 让新步骤关联上游，形成可追溯的演进路径 |
| **`action:<id>` 引用** | Action 之间自然衔接，AI 无需管理路径 |
| **数据推断 Schema** | 从 Excel 转换时一并生成，AI 可快速理解字段结构与取值 |
| **纯 Go 单二进制** | 部署简单，交叉编译无障碍 |
| **内存 SQLite** | 复用成熟 SQL 引擎，按整列推断类型，数值比较与排序正确 |