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
started ──► running ──► success
                    └─► failed
```

- **started**：命令开始执行时立即写入 Action 文件（`status: "running"`）。
- **success/failed**：执行结束时更新同一文件。
- 中途崩溃时，Action 停留在 `running`，AI 可通过 `status` 识别异常任务。
- Action 记录 `pid`；`running` 且进程已不存在时，`actions list` 将其显示为 `stale`，避免与真实运行中的任务混淆。
- 捕获 SIGINT/SIGTERM，尽力把状态置为 `failed`（`error.code=interrupted`）。

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

**退出码与 stdout 约定**：成功退出码 0，stdout 为结构化 JSON；失败退出码非 0（1 通用、2 参数错误、3 引用不存在、4 执行失败），**stdout 仍输出 `ErrorResponse` JSON**，人类可读日志只写 stderr。

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

---

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

```go
func ConvertExcel(input, sheet, outputDir string) (*ConvertResult, error) {
    f, err := excelize.OpenFile(input)
    rows, err := f.GetRows(sheet)
    headers := rows[0]
    records := make([]map[string]any, 0, len(rows)-1)
    for _, row := range rows[1:] {
        rec := make(map[string]any)
        for i, h := range headers {
            if i < len(row) {
                rec[h] = inferCellType(row[i])
            }
        }
        records = append(records, rec)
    }
    writeJSON(filepath.Join(outputDir, "data.json"), records)
    schema := InferSchema(records, headers) // 必有，含 updated_at
    writeJSON(filepath.Join(outputDir, "data.schema.json"), schema)
    return &ConvertResult{...}, nil
}
```

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
3. 环境变量 `DTOOL_FONT`
4. 自动发现系统字体：扫描平台字体目录（Linux `/usr/share/fonts` 等、macOS `/System/Library/Fonts` 等、Windows `%WINDIR%\Fonts`），按文件名关键字（Noto Sans CJK、文泉驿、微软雅黑、苹方、黑体…）排优先级，逐个校验可解析，并**优先选同时含汉字与数字字形的字体**（仅 CJK 的字体如 Droid Sans Fallback 只作兜底，并给出警告，否则坐标轴数字会显示为方框）。

支持 `.ttf` 与 `.ttc`（自动抽出第一个字体）；CFF 轮廓的 `.otf`（如 Noto Sans CJK 的 OTF/OTC）渲染库无法解析，会被跳过。仅当标题/标签含非 ASCII 字符时才触发字体解析；显式指定的字体不可用时直接报错，不静默回退。

**配置文件**（`-c config.yaml`，命令行参数优先；相对路径相对配置文件所在目录，支持 `~/`）：

```yaml
font: /usr/share/fonts/truetype/arphic/uming.ttc
workspace: .dtool
preview_rows: 20
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
│   │   └── load.go         # JSON → 内存 SQLite
│   ├── visualize/
│   │   └── chart.go
│   ├── formatter/
│   │   ├── json.go
│   │   ├── csv.go
│   │   └── markdown.go
│   └── pipeline/
│       └── runner.go
├── pkg/types/
│   └── result.go
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
- **崩溃中断**：Action 停留在 `running`（`pid` 已不存在则显示 `stale`），AI 通过 `actions list --status running` 可发现并重试。
- **沙箱拒绝**：SQL 访问被禁止的路径时，返回 `sandbox violation` 并说明允许的范围。
- **超时/超限**：`--timeout` 或 `--max-rows` 触发时返回明确错误码，而不是截断后假装成功。
- **引用失效**：`action:<id>` 指向不存在的 Action 时，返回 `action not found` 并列出最近可用的 Action ID。

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