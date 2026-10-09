---
name: dtool
description: 本地数据 Pipeline CLI。用于把 Excel 转成可复用的数据集（JSON + Schema），用 SQL 查询，输出表格（CSV/Markdown/Excel）或图表（PNG/SVG），并把每一步记录为可追溯的 Action。当用户要分析 Excel 数据、做汇总/图表/导出，或追问"之前的结果/进度"时使用。
---

# dtool 使用指南（给 AI Agent）

单二进制、无外部依赖。所有命令的 **stdout 都是结构化 JSON**，失败时 stdout 仍输出错误 JSON、退出码非 0，人类可读信息写 stderr。

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
| `datasets list \| show <name> \| delete <name>` | 查阅/删除数据集；回答"有哪些数据、长什么样"用这个 |
| `query --sql ... [--source 别名=引用]... [--format json\|csv\|markdown\|table\|xlsx] [--output f] [--max-rows N] [--timeout 60s] [--load-mode auto\|stream\|full]` | `xlsx` 必须配 `--output`；`--from <ref>` 仅记录血缘 |
| `visualize --input <ref> --type bar\|line\|pie\|table --x X --y Y [--format png\|svg] [--output f] [--font f.ttf]` | `table` 类型用 `--format md\|xlsx`，无需 x/y；y 必须是数值列 |
| `pipeline --excel f \| --input ref [--sql ...] [--chart ...]` | `--chart` 必须有 `--sql`；SQL 里用 `data` 指代上游数据 |
| `actions list [--limit N --type T --status S]` | 最新在前；状态含 `stale`（进程已死的 running） |
| `actions show <id> \| output <id> \| trace <id> \| annotate <id> --text ... --by ai-agent \| export \| reindex` | `annotate` 把用户口径/备注写进 Action |
| `--update [--pre]` / `upgrade [--version V] [--pre]` | 检查更新 / 升级自身，见文末 |

通用参数：`--tags a,b`、`--notes`、`--from <ref>`、`--preview-rows N`、`--no-record`（不记录、不可被引用）、`--load-mode auto|stream|full`、`--max-memory 2G`（`0` 关闭检查）、`-c config.yaml`。

## SQL 规则（最常踩坑）

- 表名：**数据集名**、`--source 别名=引用` 的别名，或**双引号/反引号**包裹的文件路径。**单引号无效**（那是字符串）。含中文/特殊字符的列名用双引号：`SUM("销量")`。
- 引擎是内存 SQLite：数值列按**整列**推断类型，`WHERE amount > 60`、`ORDER BY` 按数值比较；日期是 `TEXT`（ISO 格式），用 `date()` 等函数或字符串比较。
- 沙箱默认开启：只允许单条 `SELECT`/`WITH`；文件只能来自工作区、当前目录或 `--source`。不要尝试 `PRAGMA`/`ATTACH`/多语句。
- 结果过大会报错（`--max-rows`，默认 10000）：加 `LIMIT` 或先聚合。

## 读取结果的方式

- 默认只读 Action 的 `output.preview`（前 20 行）+ `columns` + `summary`；`preview_truncated: true` 才需要读完整产物（`actions output <id>`）。
- 产物路径都是相对项目目录的：数据集 `.dtool/datasets/<name>/`，查询 `.dtool/outputs/queries/<id>/result.json`，图表 `.dtool/outputs/charts/<id>/`。

## 对话场景

| 用户说 | 做法 |
|--------|------|
| "进度怎么样 / 刚才做了什么" | `actions list --limit 5`，用 `summary` 回答 |
| "为什么失败了" | `actions list --status failed --limit 1` → `actions show <id>`，读 `error`（含 `detail`/`hint`）；字段缺失时 `detail` 会列出可用列 |
| "用上次的结果画图/导出" | `visualize --input latest:query ...`，不要重跑查询 |
| "按季度再拆一下" | 新 `query`，带 `--from action:<上次id>`（血缘），数据源用 `dataset:` 或 `--source` |
| "标记这个口径含税" | `actions annotate <id> --text "口径：含税" --by ai-agent` |
| "把之前标记含税的画成图" | `actions list` 找带 `annotations` 的 Action，再引用它 |
| 一次性掌握全部上下文 | `actions export --limit 50`，读导出的 JSON |

## 错误与退出码

stdout 错误 JSON：`{"error","detail","hint","code","action_id"}`。退出码：`1` 通用，`2` 参数/用法错误，`3` 引用/文件不存在，`4` 执行失败。失败的步骤也会留下 `status: failed` 的 Action，可据此续写，无需从头来。`pipeline` 失败时 `detail` 指明失败的子步骤。

## 图表与中文

图表默认字体不含中文。字体优先级：`--font` > 配置文件 `font` > 环境变量 `DTOOL_FONT` > 自动发现的系统字体（`.ttf`/`.ttc`）。输出里的 `warnings` 提示"未找到中文字体"或"字体缺少数字字形"时，请让用户用 `--font`/配置指定 `.ttf`。配置文件示例：

```yaml
font: /usr/share/fonts/truetype/arphic/uming.ttc   # 相对路径相对配置文件，支持 ~/
workspace: .dtool
preview_rows: 20
```

## 装入方式与大数据的现实边界

JSON → 内存 SQLite 有两种装入方式：`--load-mode auto`（默认）/ `stream` / `full`。

- `auto` 按文件大小自适应：**≥32MB 走流式**；可用内存不够整块解析时也自动转流式。所以一般情况下不用管它。
- 峰值内存：`full` ≈ 文件大小 × 13；`stream` ≈ ×2。
- 1 核 2GB 下实测（默认 `auto`）：10 万行 2.4s；100 万行 19–29s；500 万行 1:29、604MB。默认参数即可，不必再调 `--timeout`。
- **Excel（`convert`）没有流式开关**：峰值 ≈ xlsx 文件大小 × 260（实测），2GB 下 6MB 左右就见顶。15 万行 / 7.7MB 的 xlsx 预估 1.9GB 会被拦下；`--max-memory 0` 强跑实测峰值 1.89GB，正好不炸。大表请拆成多个 xlsx，或先转成 JSON 再 `query`（可走 `--load-mode stream`）。
- `--timeout` 只约束**查询阶段**（默认 60s），载入耗时不计入。若内存充足、只想要速度，可用 `--load-mode full`（只解析一遍，更快）。
- 内存不足时会**快速失败并说明原因**（含「预计需 xx、可用 xx」与 `--max-memory 0` 退出口），不会静默被杀；这类失败同样留下 `failed` 的 Action。建议按场景给：`query` 推荐 `--load-mode stream`，`convert` 只建议拆分输入——它没有 `--load-mode`，照抄那个参数会白试一轮。
- 看到 stderr 的「载入 xxx.json（…，流式解析，预计需约 xx 内存）...」说明正在载入；若进程随后消失，就是内存不够。
- 需要放宽/关闭检查：`--max-memory 4G` / `--max-memory 0`，或 `DTOOL_MAX_MEMORY` 环境变量。

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
dtool --update            # 只检查，返回 {current, latest, update_available, release_url}
dtool --update --pre      # 同时考虑预览版
dtool upgrade             # 升级到最新正式版（先校验 sha256，再替换）
dtool upgrade --pre       # 允许最新预览版
dtool upgrade --version 1.2.0          # 指定版本（可降级）；预览版写 1.2.0-preview.1
```

`--version` 与 `--pre` 不能同时使用。Windows 下运行中的 `dtool.exe` 会被改名为 `.old`，新文件改名就位，下次启动自动清理；若提示文件被占用，请关闭其他 dtool 进程后重试。无写权限时在 Linux/macOS 用 `sudo`，Windows 用管理员。GitHub 限流时设置 `GITHUB_TOKEN`。
