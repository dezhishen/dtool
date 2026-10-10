# dtool

纯 Go、无 CGO、单二进制的本地数据 Pipeline CLI：Excel → JSON(+Schema) → SQL → 图表/表格，每一步都记录为可追加、可派生的 Action。设计见 [Design.md](Design.md)。

```bash
# 只转换：持久化为独立数据集 .dtool/datasets/<name>/，之后可反复使用
dtool convert --input data.xlsx --name sales
dtool datasets list                 # 查看已有数据集
dtool datasets show sales           # 位置 + Schema + 预览
dtool query --sql 'SELECT region, SUM(amount) FROM sales GROUP BY region'   # 数据集名直接当表名

# 复用已持久化的数据查询（无需重新转换）
dtool query --source sales=dataset:sales --sql 'SELECT region, SUM(amount) AS total FROM sales GROUP BY region'

# 一条龙（各步骤可选）：
dtool pipeline --excel data.xlsx                                   # 仅转换
dtool pipeline --input latest:convert --sql 'SELECT * FROM data'   # 复用数据 + 查询
dtool pipeline --excel data.xlsx --sql 'SELECT region, SUM(amount) AS total FROM data GROUP BY region' \
  --chart bar --x region --y total --font ./font.ttf

dtool actions list --limit 5
```

导出：`query --format csv|markdown|table|json|xlsx --output <文件>`（xlsx 需 `--output`）；`visualize --type bar|line|pie --format png|svg` 出图；`visualize --type table --format md|xlsx` 导出表格。

进程被强杀（SIGKILL / 被 OOM 杀）时，dtool 起的看护进程会在约 0.25s 内把这次命令遗留的 Action
收敛为 `stale`（`error.code=5`，说明「进程已消失、结果未知」），无需再跑命令；`DTOOL_NO_REAPER=1` 可关闭。

目录：`.dtool/datasets/<name>/`（数据集，独立于 Action；同名重新转换会覆盖数据与 Schema 并刷新更新日期；Schema 必有）、`.dtool/outputs/queries/<id>/`、`.dtool/outputs/charts/<id>/`、`.dtool/actions/`。

SQL 表名：用 `--source 别名=引用`，或用**双引号**包裹文件路径（单引号无效）。数值列按整列推断类型，`WHERE`/`ORDER BY` 按数值比较。

## 安装

从 [Releases](https://github.com/dezhishen/dtool/releases) 下载对应平台的包（单二进制、无外部依赖，Linux/macOS/Windows × amd64/arm64）：

| 平台 | 包名 |
|------|------|
| Linux / macOS | `dtool_<版本>_<os>_<arch>.tar.gz` |
| Windows | `dtool_<版本>_windows_<arch>.zip`（内含 `dtool.exe`） |

包内除可执行文件外还有 `README.md`、`SKILL.md`、`LICENSE`、`THIRD_PARTY_NOTICES.md`；其中 `SKILL.md`
就是仓库里那份 Agent Skill 包 [skills/dtool/SKILL.md](skills/dtool/SKILL.md)（解包后可直接丢进平台的技能目录）。
v0.2.0 及以前的归档里它叫 `skills.md`，`dtool upgrade --skills` 两种名字都能取。

```bash
# Linux / macOS（以 linux-amd64 为例，macOS 换成 darwin_amd64 或 darwin_arm64）
V=v0.2.1                                              # 最新版本；预览版形如 v0.3.0-preview.1
base=https://github.com/dezhishen/dtool/releases/download/$V
curl -LO "$base/dtool_${V#v}_linux_amd64.tar.gz"
curl -LO "$base/checksums.txt"
sha256sum -c --ignore-missing checksums.txt           # macOS 用 shasum -a 256 -c
tar -xzf "dtool_${V#v}_linux_amd64.tar.gz"
sudo install -m 0755 dtool /usr/local/bin/            # 放进 PATH
dtool version                                         # 确认可用
```

```powershell
# Windows（PowerShell）
$V = "v0.2.1"                                        # 最新版本
$base = "https://github.com/dezhishen/dtool/releases/download/$V"
Invoke-WebRequest "$base/dtool_$($V.TrimStart('v'))_windows_amd64.zip" -OutFile dtool.zip
Invoke-WebRequest "$base/checksums.txt" -OutFile checksums.txt
Expand-Archive dtool.zip -DestinationPath .
.\dtool.exe version                                   # 确认可用
```

不想每次手改版本号时，可从 `https://api.github.com/repos/dezhishen/dtool/releases/latest` 取 `tag_name`（只含正式版；要预览版就换成 `.../releases` 列表的第一项）。

请装到**有写权限**的目录：`dtool --update [--pre]` 检查新版本、`dtool upgrade [--pre]` 会替换自身（详见下文）。

## 版本与构建元数据

```bash
dtool --version            # dtool version 1.2.0 (commit abc1234, built 2026-10-09T01:00:00Z, go1.27.1 linux/amd64)
dtool version              # JSON：version / channel / commit / commit_date / branch / dirty / build_date / build_id / build_url / builder / repo / go / compiler / cgo / os / arch
dtool version --short      # 只输出版本号
dtool version --deps       # 附带编译进二进制的依赖模块及版本
```

元数据在打包时由 `scripts/ldflags.sh` 注入（`make build`、`make build-all`、`make release-build` 和 Release 流水线共用同一份），本机 `go build` 则回退到 Go 嵌入的 VCS 信息。`channel` 由版本号判定：`X.Y.Z` 为 `stable`，`X.Y.0-preview.N` 为 `preview`，`dev-*` 为 `dev`，其余为 `local`。

**main 分支的测试构建**：CI 在**每 4 小时**定时构建全部平台（也可在 Actions 页面手动触发），版本号为
`dev-<Actions run id>`，并**滚动发布**到固定 tag `dev`（同名资产每次覆盖，只保留最新一次）。
所以 `dtool upgrade --channel dev` 装的就是它；`stable` / `preview` 两个渠道完全看不到 dev 构建。
每次构建同时留一份 workflow artifact（`dtool-dev-<run id>`，14 天），`dtool version` 里的 `build_url`
可直接跳转到对应的运行记录。

## 示例

[examples/](examples/README.md) 提供 4 个可直接运行的示例，数据源在 `examples/data/`。
每个示例目录下都有一份 README，写清「什么场景下用它、逐步做了什么、产物是什么、有哪些坑」：

| 示例 | 什么场景 | 数据源 | 产物 |
|------|----------|--------|------|
| [01 销售报表](examples/01-sales-report/README.md) | 多工作表工作簿 → 拆成多张表 → JOIN → 出图 + 导 Excel | `sales.xlsx`（订单 120 行、客户 15 行） | `region.md`、`region.png`、`top_customers.xlsx` |
| [02 人员分析](examples/02-hr-analysis/README.md) | 花名册：前导零工号、可空列、日期过滤 | `hr.xlsx`（员工 60 行） | `headcount.png`、`recent_hires.csv`、`no_rating.xlsx` |
| [03 脏数据处理](examples/03-messy-data/README.md) | 手工做的脏 Excel：先看自动修正清单，再用 SQL 清洗 | `messy.xlsx`（故意做脏） | 数据集与 `warnings`（控制台输出） |
| [04 pipeline 与 Action](examples/04-pipeline-actions/README.md) | 一条命令出图、复用数据集、口径注释与血缘追溯 | `sales.xlsx` | `trend.png`、`.dtool/actions/` |

```bash
make build && bash examples/run-all.sh     # 全部运行，输出在 examples/out/<示例名>/
bash examples/01-sales-report/run.sh       # 只跑单个
```

## 场景手册（按职业）

不知道从哪下手时，从自己的职业看起：[docs/scenarios/](docs/scenarios/README.md) 每个职业一份文档，
从「一条 SQL 拿到答案」递进到「连表 + 出图 + 导出 + 留痕」，命令都能对着示例数据直接跑。

表里的「对 AI 说」一列可以直接当成给 Copilot / 其他 Agent 的提示词（把文件名换成你自己的即可）：

| 场景 | 典型问题 | 对 AI 说（示例） |
|------|----------|------------------|
| [销售 / 业务运营](docs/scenarios/sales.md) | 各区卖了多少？哪些客户在贡献增长？ | 按区域汇总 `sales.xlsx` 的订单销售额并出柱状图，再给我金额 Top 5 客户的 Excel |
| [财务 / 会计](docs/scenarios/finance.md) | 收入口径对不对？有没有对不上的账？ | 核对 `sales.xlsx` 的金额是否等于单价 × 数量，列出退款明细导出 CSV，再出月度净收入对账表 |
| [HR / 人事](docs/scenarios/hr.md) | 各部门薪酬结构？谁是待评估新人？ | 汇总 `hr.xlsx` 各部门人数与平均月薪出饼图，并导出绩效为空的人员名单 |
| [电商 / 仓储运营](docs/scenarios/ecommerce.md) | 哪些 SKU 是爆款？退款率、复购如何？ | 统计 `sales.xlsx` 各产品销量与退款率，再找出各区域销量前二的品类 |
| [市场 / 增长](docs/scenarios/marketing.md) | 增长从哪来？环比、新客、客单价？ | 看 `sales.xlsx` 的月度销售额趋势与环比，再统计新客首单月份和城市分布，各出一张图 |
| [管理者 / 汇报](docs/scenarios/management.md) | 5 分钟给出一页图表，还要说得清来源 | 用 `pipeline` 把 `sales.xlsx` 一条命令出成月度折线图并标注口径，之后我要能追溯它是怎么算出来的 |
| [数据 / BI](docs/scenarios/analyst.md) | 手上是 JSON、大文件、脏数据怎么查？ | 把这个 1.5GB 的 JSON 用省内存的方式读进来查某列分布并导出 CSV；若是脏 Excel，先告诉我自动改了哪些 |

## 装入方式与大数据量

JSON → 内存 SQLite 的装入方式由 `--load-mode`（`auto`（默认）/ `stream` / `full`）和
`--store`（`auto`（默认）/ `memory` / `disk`）组合成三个执行档：

| 执行档 | 峰值内存 | 相对速度 | 适用 |
|--------|----------|----------|------|
| `full` + 内存库 | ≈ 文件大小 × 13 | 最快 | 小文件（<32MB 且峰值 ≤ 预算 80%） |
| `stream` + 内存库 | ≈ 文件大小 × 2 | 略慢（解析两遍） | 大文件、低内存 |
| `stream` + 磁盘库 | ≈ 文件大小 × 0.5 | 最慢（临时表落盘） | 内存极紧 |

`auto` 按**内存预算**在阶梯上选档（不是按单一阈值硬切），stderr 与 Action 里都写明选了哪档、为什么：

1. 输入 <32MB 且整块解析的预计峰值 ≤ 预算的 80% → `full` + 内存库；
2. 否则整块解析峰值 ≤ 预算 → `stream` + 内存库；
3. 再不行 → `stream` + 磁盘库（峰值最低）；
4. 连最省档都放不下 → 按 `--mem-policy`：`try`（默认）仍试最省档（失败记入 Action，AI 可读），
   `strict` 直接失败。

两条硬规则：**整块解析只在输入 <32MB 时考虑**（它的峰值是输入的 13 倍，再大这笔放大不划算，流式只有 ~2 倍）；
**估算必须留 20% 余量**（擦着预算通过时，实际很容易在中途被看门狗中止）。选档还会用本机历史样本
（`.dtool/plans/samples.json`）校准，见 `docs/PERFORMANCE.md`。

这套开关只作用于 **JSON 装入**（`query` / `pipeline`）。`convert` 读 Excel 本身就是流式的两阶段解析，
没有 `--load-mode` 可切（传了不报错但会被忽略，CLI 会在 stderr 提示），
它的内存只随**文件体积**增长，见下一节。

1 核 2GB 实测（cgroup 2GB + 单核 + 禁 swap）：17MB / 10 万行 → 3.2s、236MB；
456MB / 500 万行 → 1:31、603MB；1.34GB / 1400 万行 → 3:42、1637MB（需 `--max-memory 0`，
默认预检会在约 850MB 以上提前拦下）。

流式模式下内存不再随文件线性暴涨，限制主要变成**耗时**。`--timeout` 只约束**查询阶段**（默认 60s）；载入是本地的读写与 CPU 工作，不受它限制，由内存看门狗和 Ctrl+C 兜底——按下 Ctrl+C（或收到 SIGTERM）会立刻停在该步，以 `code: 5`「已中断」退出并留下 `failed` 的 Action（`4` 才是「跑完但出错」，见 [skills/dtool/SKILL.md](skills/dtool/SKILL.md)）。

```bash
dtool query --load-mode stream --source d=big.json --sql 'SELECT ...'
dtool query --load-mode full   --sql '...'    # 内存充足时求快
dtool query --timeout 10m      --sql '...'    # 查询本身很重时再调大（默认 60s）
```

### Excel 输入的现实边界

`convert` 是**流式**的（两阶段、单次解析：逐行读单元格累积列统计并落临时 JSONL，再按推断出的 Schema 逐行写成 `data.json`），峰值内存 ≈ **32MB + 文件 × 3**，与行数无关：7.4MB / 15 万行 × 9 列 → 43MB；53.3MB / 105 万行 × 8 列（Excel 单表行数上限）→ **72MB、1:10**。

预检按「32MB + 文件 × 6」估算（约为实测的 1.7 倍），超出预算时会在转换前拦下并给出数字。上一个版本这里是 ×260：同一个 7.4MB 文件要 1.9GB，17.8MB 的文件在 2GB 上限下直接被 OOM 杀掉。

### 性能测试

**测试环境、方式、全部结果与 1 核 2GB 的能力边界见 [docs/PERFORMANCE.md](docs/PERFORMANCE.md)。** 回归用例随 `go test ./...` 一起跑（`-short` 跳过）：

| 用例 | 断言 |
|------|------|
| `TestPerfLoadModeMemoryRatio` | 流式装入的存活堆峰值须比整块解析低 1.3 倍以上（实测约 9 倍） |
| `TestPerfConvertExcelMemory` | Excel 转换的峰值堆不得超过预检估算（`xlsxNeed(文件) × 1.3`），防止重新引入整表物化这类量级回归 |

吞吐用基准看，需要显式开启（数值见 [docs/PERFORMANCE.md](docs/PERFORMANCE.md)）：

```bash
make bench                            # JSON 2 万行 / xlsx 4 万行
DTOOL_BENCH_ROWS=200000 make bench    # 放大行数
go test ./internal/query -bench LoadStream -benchmem   # 只看某一项
```

## 内存不足时的行为

为避免「进程被内核静默杀掉、用户不知道发生了什么」，dtool 会：

- **按预算选执行档**：内存上限用来**选档**（`full+memory` → `stream+memory` → `stream+disk`），
  而不是「过/不过」——磁盘库那一档把同一份 123MB 输入的峰值从 176MB 压到 20MB（耗时几乎不变），
  所以受限环境里通常是"换个档照跑"而不是报错。只有连最省档都预计放不下时才触及失败路径，
  且默认策略仍会试一次并把结果记入 Action（`--mem-policy strict` 可改为直接失败）。**建议按场景给**：`query` 可切 `--load-mode stream`（整块解析 ≈ 文件 × 13）；`convert` 不需要这个开关（转换只有一个流式实现，峰值只随文件体积增长；传了不生效，CLI 会在 stderr 提示），提示只会让它缩小输入或放宽预算；两者都保留 `--max-memory 0` 强制运行口；
- **运行期看门狗**：载入/查询期间监控本进程内存，超出预算时先在 stderr 打印「内存超出预算（已用 xx，预算 xx），正在中止」，再以普通错误中止并写入一条 `failed` 的 Action（整块解析也能被中止，不再长时间无输出）；
- **留余量**：`auto` 只有在整块解析峰值 ≤ 预算 80% 时才选它，否则走流式；显式 `--load-mode full` 且逼近预算时会提前提示；
- **载入进度**：数据源 ≥8MB 时向 stderr 打印「载入 xx（大小，装入方式，预计需约 xx 内存）...」，即使进程被强杀也能看出卡在哪里。

排查用 `dtool meminfo [--source alias=文件]`（打印预算来源、原始探测字段、预检预演，
并在探测不可信时给出 `warnings`）或
`DTOOL_DEBUG_MEMORY=1`（把每个命令的判定过程打到 stderr）。

**探测来源**（决定上面这些预检/看门狗的数字从哪来）：Linux 读 cgroup v2/v1 与系统可用内存；
Windows 读 **Job Object 的内存上限**（`JOB_OBJECT_LIMIT_PROCESS_MEMORY` 0x100 与
`JOB_OBJECT_LIMIT_JOB_MEMORY` 0x2000，两者都设时取更小值；CI/沙箱常用）与系统可用内存，
进程用量按**私有提交量**（commit charge）计算；macOS 等平台不做自动探测，
请用 `--max-memory` 显式给出——否则预检与看门狗都处于关闭状态，只能等外部硬上限把分配打回来。

读到上限之后还要**真的用上**：Job Object 的读取以 `QueryInformationJobObject` 为准，`IsProcessInJob`
只作佐证（它返回 0 既可能是「不在 Job 里」也可能是调用失败，当闸门会让探测静默失效）。
读不到时 `meminfo` 的 `warnings` 会直说「预算回落到系统可用内存，请用 `--max-memory`」，
不会再安静地按 16GB 继续跑。

Go 堆软上限只取可用预算的 **3/4**：软上限只管 Go 堆，而 SQLite 页缓存是 mmap/VirtualAlloc 出来的、
同样计入提交量，堆按 100% 走就会把提交顶到硬上限。

⚠️ 关闭检查（`--max-memory 0`）或平台探测不到上限时，顶到硬上限的失败方式**不可控**：
可能是可读的结构化错误（`内存不足：…SQLite 分配失败`，code 4，驱动原文 `out of memory (7)`），
也可能是 Go runtime 的 `fatal error: out of memory`（**不可恢复**，会打印 goroutine 堆栈、退出码 2）。
根因是 modernc/SQLite 的页缓存走 mmap/VirtualAlloc，不受 Go 堆软上限约束，
所以「给得出预算」比「靠运气」重要——容器里请显式 `--max-memory`（或让 dtool 读到 cgroup/Job Object）。可用全局参数覆盖：

```bash
dtool query --max-memory 4G --sql '...'   # 显式预算
dtool query --max-memory 0  --sql '...'   # 关闭检查（内存不足时仍会被系统杀掉）
DTOOL_MAX_MEMORY=2G dtool pipeline ...    # 环境变量，适合容器/CI
```

配置文件同样支持：

```yaml
load_mode: auto      # auto / stream / full
store: auto          # auto / memory / disk（SQLite 库落在哪）
mem_policy: try      # try（仍试最省档）/ strict（直接失败）
```

需要**强行指定**而不靠自适应时（排查、对比、或你就是想快）：

```bash
dtool query --load-mode full --sql '...'    # 强制整块解析（峰值高，预算不足会自行降档前先试）
dtool query --store disk  --sql '...'       # 强制磁盘库（峰值最低，硬上限下最稳）
dtool query --load-mode full --store disk   # 报用法错误：full 的峰值在 Go 堆，换库不省
dtool query --mem-policy strict --sql '...' # 连最省档都放不下时直接失败，不试
```

估算倍率是**上界**，平台差异可以很大（同一份 12MB 输入：Linux 上内存档 1.4×，
Windows 沙箱里实测到 ≈15×）。所以估算会**按本档历史实测上修**（8 倍封顶，只用同档样本）：
第一次撞上看门狗后写一条失败样本，第二次就会自动降到磁盘档——**同一台机器、同一个输入
只付一次试错代价**。`meminfo` 的 `plan.rungs` 里能看到 `raw_need` 与校准后的 `need`。

Windows 上若 Job Object 只置了标志位、值读回 0（`job_object.limit_unreadable`），
预算就只是「本机空闲内存」，**不可信**：此时会明确警告并排除整块解析档（峰值比输入大
一个数量级的那一档），必要时用 `--store disk` 进一步压低峰值。

每次实际用的档会写进结果（`strategy` / `strategy_note`）与 Action，`meminfo` 会列出
整条阶梯的预计峰值与历史成功率。执行的实测结果按「预计/实测」记入
`.dtool/plans/samples.json`（`--no-record` 时不写）：**同一台机器、同一个输入只付一次
试错代价**，下次直接命中能过的档。

## 检查更新与升级

三个渠道，`--channel` 选（`--pre` 是 `--channel preview` 的旧写法）：

| 渠道 | 含义 | 例子 |
|---|---|---|
| `stable`（默认） | 最新正式版 / 补丁版 | `dtool upgrade` |
| `preview` | 正式版 + 预览版（`vX.Y.0-preview.N`） | `dtool upgrade --channel preview`（或 `--pre`） |
| `dev` | **main 的最新构建**（滚动发布） | `dtool upgrade --channel dev` |

```bash
dtool --update [--channel stable|preview|dev]   # 只检查，不改动任何文件
dtool upgrade [--channel dev]                   # 升级到该渠道的最新版
dtool upgrade --version 1.2.0                   # 指定版本（可降级）；预览版如 1.2.0-preview.1
dtool upgrade --version dev                     # dev 构建也可显式指定（滚动发布只保留最新一次）
dtool upgrade --skills                          # 顺带取该版本的 SKILL.md；裸写法写到当前目录
dtool upgrade --skills=docs/                    # 或写进指定目录（不存在会创建）/ --skills=agent.md 指定文件
```

`--skills[=路径]`：把**你装的那个版本**的 `SKILL.md`（面向 AI Agent 的使用手册）另存一份——取值是目录就写
`<目录>/SKILL.md`（目录不存在会创建），是 `.md` 文件就写该文件，裸 `--skills` 写当前目录。手册取自发布归档，
和二进制是同一份资产、同一次 sha256 校验，不会出现「二进制是旧版、手册是新版」；已经是最新版本时也能单独取
（完全不碰二进制），JSON 里的 `skills_changed` 说明内容有没有变化。

`dev` 渠道的特殊之处：dev 构建之间**没有版本序**（`dev-<run id>` 不是语义化版本），所以
它比的是**构建身份**——发布里的 `dev-build.txt` 记着当前构建号，和二进制里的
`version`/`build_id` 一致就认为已是最新，不会因为「版本号看起来更小」而反复重装或降级。
dev 发布是**滚动**的（固定 tag `dev`，每次 main 构建覆盖同名资产），因此不会堆出成百上千个
Release，也不需要额外的清理策略。**只保留最新一次**：想装某次特定构建的制品，用那一次
workflow 的 artifact（`gh run download <run id> --name dtool-dev-<run id>`，14 天）。

`dev` 是**移动 tag**（每次构建都落到 main 最新提交），因此它被显式排除在「版本号」与「发版
对比基线」的计算之外（`scripts/version.sh`、`scripts/release-info.sh`）：否则本地构建会变成
`dev`/`dev-1-g<sha>`（被误认为 dev 渠道构建），发版说明的基线也会退化成移动的 `dev`。
`scripts/tests/dev-tag-hygiene.test.sh` 用「dev 比版本 tag 更近」的恶意布局守住这条。

发布时机（`.github/workflows/dev-release.yml`）：**每 4 小时**定时一次，或在 Actions 页面**手动触发**
（`gh workflow run dev-release.yml`，`-f force=true` 可忽略缓存强制重发）。**刻意不挂 push**——
否则每次提交都要跑一轮六平台构建 + 覆盖上传；想「推完立刻要 dev」就手动触发一次。两种触发都会先比对
`dev` 发布里 `dev-build.txt` 记录的 commit——**commit 没变就跳过**，不重复构建、不滚动构建号。

升级前会校验 Release 中 `checksums.txt` 的 sha256，并先运行新二进制自检；任何一步失败都不会改动现有文件。Windows 下运行中的 exe 不能覆盖，因此先把旧文件改名为 `.old` 再让新文件就位（失败回滚，`.old` 下次启动自动清理）。GitHub 限流时设置 `GITHUB_TOKEN`；`DTOOL_REPO` / `DTOOL_UPDATE_API` 可指向镜像。

GitHub 连接偶发中断（`EOF`、`connection reset`、5xx、429 等）会自动重试 3 次，间隔按**指数退避** 500ms → 1s → 2s（上限 10s）；瞬时失败只要成功一次就继续。重试仍失败时会说明「已尝试几次」，并给出无需本工具的手动下载入口：

```text
https://github.com/dezhishen/dtool/releases   # 自行下载对应平台的压缩包，替换二进制即可
```

即：`upgrade` 不会因为一次网络抖动就放弃，失败信息里也直接带上 Release 页面地址，不必再靠工具自己去猜。

## 给 AI Agent / 技能平台

agent 手册的源文件是 **[skills/dtool/SKILL.md](skills/dtool/SKILL.md)**——按主流平台约定的
Agent Skill 布局组织（一个技能一个目录，`SKILL.md` 带 `name`/`description` frontmatter，
目录名与 `name` 一致）：

```
skills/dtool/SKILL.md      # name: dtool；description 说明"什么时候该用它"
```

装法三选一：

```bash
cp -r skills/dtool ~/.claude/skills/                 # 或平台的技能目录
cp -r skills/dtool .github/skills/                   # 项目内（VS Code / Copilot 也会读）
dtool upgrade --skills=~/.claude/skills/dtool/SKILL.md   # 从发布归档里取，版本与二进制同源
```

最后一条是给"已经装了 dtool"的场景：手册和二进制来自同一份发布、同一次 sha256 校验，
不会出现「二进制是 0.2.0、手册是 main」。裸 `--skills` 等价于写到当前目录的 `./SKILL.md`。

反向不成立：**`v0.2.0` 的旧二进制不认识新归档里的 `SKILL.md`**（那时候它只按 `skills.md` 找），
所以「0.2.0 的 dtool + `--skills` + 0.2.1 的归档」会报「归档里没有 skills.md」——而且**不会替换
二进制**（手册在替换之前写，失败即中止，不留半成品）。做法很简单：先不带 `--skills` 升级一次，
之后新二进制就能正常取手册了。

## 中文图表与配置文件

字体优先级：`--font` > `-c config.yaml` 的 `font` > `DTOOL_FONT` > 自动发现的系统中文字体（支持 `.ttf`/`.ttc`）。

```yaml
# config.yaml（命令行参数优先；相对路径相对此文件）
font: /usr/share/fonts/truetype/arphic/uming.ttc
workspace: .dtool
preview_rows: 20
```

```bash
dtool -c config.yaml visualize --input latest:query --type bar --x 区域 --y 销量 --title 区域销量
```

## 开发

```bash
make check               # gofmt + vet + test
make build               # 当前平台
make build-windows-arm64 # 单个平台（linux/darwin/windows × amd64/arm64）
make build-all           # 全部平台二进制 -> dist/bin/
make release-build       # 全部平台压缩包 + checksums.txt -> dist/（Release 同款）
```

## 发版

tag 即发版，推送后由 `.github/workflows/release.yml` 校验、测试、构建并创建 Release；说明自动汇总上一基线到当前 tag 之间的 PR 与 commits。

| 类型 | tag | 说明 |
|------|-----|------|
| 预览版 | `vX.Y.0-preview.N` | 打在 `main`；标记为 prerelease；基线为最近的任意 tag |
| 正式版 | `vX.Y.0` | 打在 `main`；基线为上一个正式/补丁版（汇总整个预览期） |
| 补丁版 | `vX.Y.Z`（Z≥1） | 打在 `main` 或 `release/X.Y`；要求 `vX.Y.0` 已存在 |

**预览版本号的「排序错觉」**：GitHub 的 Releases 列表是按 **tag 名**（字典序）排的，不是发布时间，所以
`v0.2.0-preview.10` 会显示在 `v0.2.0-preview.9`、甚至 `v0.2.0-preview.1` 的**下面**——看着像"没排对"。
不要用零填充（`preview.010`）去凑字典序：semver 明确规定数字标识符不得有前导零，这种 tag 会被
`scripts/release-info.sh` 直接拒绝（CI 发版失败），也会被 `internal/updater` 的解析跳过 —— 反而让这个
版本对 `--update` / `upgrade` **完全不可见**。列表顺序只是展示问题：选版一律按语义化版本比较，与列表顺序
无关（`TestLatestIgnoresReleaseListOrder` 按 GitHub 的真实返回顺序钉住这一点）。

**预览版会随正式版发布自动清理**：`.github/workflows/prune-previews.yml` 每天定时跑一次
（也可手动触发，手动触发默认 `dry_run=true` 只打印不删），删除「对应正式版已经发布」且**正式版发布满 14 天**
的预览版 Release 与 tag（`--retention-days` 可调，`0` 表示正式版一发就清理）。
只处理 `vX.Y.0-preview.N`，不会碰正式版 `vX.Y.0` / 补丁版 `vX.Y.Z`。

```bash
bash scripts/prune-preview-releases.sh --dry-run              # 本地看看会删什么（不发请求）
bash scripts/prune-preview-releases.sh --dry-run --retention-days 0
make scripts-test                                            # 选择规则（含保留期边界）单测
```

## 许可证

dtool 以 [MIT](LICENSE) 协议发布。依赖均为宽松协议（MIT / BSD-3-Clause / Apache-2.0；`golang/freetype` 双协议中选用 FreeType License），不含 GPL/LGPL/MPL 代码。

第三方版权声明与许可证全文（`THIRD_PARTY_NOTICES.md`）在每次打包时由 `scripts/gen-notices.sh` 根据当前依赖自动生成，并随发布压缩包分发；它不提交到仓库，升级依赖无需额外操作。需要时可运行 `make notices` 本地生成。
