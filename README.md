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

目录：`.dtool/datasets/<name>/`（数据集，独立于 Action；同名重新转换会覆盖数据与 Schema 并刷新更新日期；Schema 必有）、`.dtool/outputs/queries/<id>/`、`.dtool/outputs/charts/<id>/`、`.dtool/actions/`。

SQL 表名：用 `--source 别名=引用`，或用**双引号**包裹文件路径（单引号无效）。数值列按整列推断类型，`WHERE`/`ORDER BY` 按数值比较。

## 版本与构建元数据

```bash
dtool --version            # dtool version 1.2.0 (commit abc1234, built 2026-10-09T01:00:00Z, go1.27.1 linux/amd64)
dtool version              # JSON：version / channel / commit / commit_date / branch / dirty / build_date / build_id / build_url / builder / repo / go / compiler / cgo / os / arch
dtool version --short      # 只输出版本号
dtool version --deps       # 附带编译进二进制的依赖模块及版本
```

元数据在打包时由 `scripts/ldflags.sh` 注入（`make build`、`make build-all`、`make release-build` 和 Release 流水线共用同一份），本机 `go build` 则回退到 Go 嵌入的 VCS 信息。`channel` 由版本号判定：`X.Y.Z` 为 `stable`，`X.Y.0-preview.N` 为 `preview`，`dev-*` 为 `dev`，其余为 `local`。

**main 分支的测试构建**：每次推送到 `main`，CI 在测试通过后构建全部平台，版本号为 `dev-<Actions run id>`，作为 workflow artifact（`dtool-dev-<run id>`，保留 14 天）供下载测试。它不创建 tag 或 Release，所以不会被 `dtool --update` / `upgrade` 当作可升级版本；`dtool version` 里的 `build_url` 可直接跳转到对应的运行记录。

## 示例

[examples/](examples/README.md) 提供 4 个可直接运行的示例（销售报表、人员分析、脏数据处理、pipeline 与 Action 协作），含数据源文件：`make build && bash examples/run-all.sh`。

## 装入方式与大数据量

JSON → 内存 SQLite 有两种装入方式，`--load-mode` 可选 `auto`（默认）/ `stream` / `full`：

| 方式 | 峰值内存 | 相对速度 | 适用 |
|------|----------|----------|------|
| `full` 整块解析 | ≈ 文件大小 × 13 | 快 | 小文件（<32MB） |
| `stream` 流式两遍 | ≈ 文件大小 × 2 | 略慢（解析两遍） | 大文件、低内存 |

`auto` 按实际文件大小自适应：**≥32MB 自动转流式**；若探测到可用内存不足以整块解析（×13 超预算），也自动转流式。

1 核 2GB 实测（同一台机器，限制为 cgroup 2GB + 单核）：

| 数据 | 旧版（只有整块解析） | `auto` |
|------|----------------------|--------|
| 17MB / 10 万行 | 3.2s，267MB | 2.4s，275MB（走 full） |
| 104MB / 100 万行日志 | 16.5s，1543MB | 19s，**157MB**（走 stream） |
| 168MB / 100 万行订单 | **被 OOM 杀掉** | 29s，**183MB** |
| 456MB / 500 万行 | **被 OOM 杀掉** | 1:29，**604MB** |

流式模式下内存不再随文件线性暴涨，限制主要变成**耗时**。`--timeout` 只约束**查询阶段**（默认 60s）；载入是本地的读写与 CPU 工作，不受它限制，由内存看门狗和 Ctrl+C 兜底。

```bash
dtool query --load-mode stream --source d=big.json --sql 'SELECT ...'
dtool query --load-mode full   --sql '...'    # 内存充足时求快
dtool query --timeout 10m      --sql '...'    # 查询本身很重时再调大（默认 60s）
```

### Excel 输入的现实边界

`convert` 用 excelize 整表解析，**没有流式开关**，峰值内存 ≈ xlsx 文件大小 × 260（xlsx 是压缩容器，解压后膨胀很大）：

| xlsx 大小 | 预估峰值 | 1 核 2GB |
|-----------|----------|----------|
| 1MB | 260MB | 可以 |
| 6MB | 1.6GB | 接近上限 |
| 7.7MB / 15 万行 × 6 列 | 1.9GB | **载入前被拦下**（`--max-memory 0` 强跑实测峰值 1.89GB） |

大表建议先拆成多个 xlsx 分别导入，或先转成 JSON 再用 `dtool query`（走 `--load-mode stream`，峰值降到 ×2）。

### 性能回归测试

上表的结论不只写在文档里，也在测试里，随 `go test ./...` 一起跑（`-short` 跳过）：

| 用例 | 断言 |
|------|------|
| `TestPerfLoadModeMemoryRatio` | 流式装入的峰值堆须比整块解析低 1.3 倍以上（实测 2.3–3.7 倍） |
| `TestPerfConvertExcelMemory` | Excel 转换的峰值堆不得超过预检倍率 `×260 × 1.3`，防止「实际变差而预检没跟上」导致静默 OOM |

吞吐用基准看，需要显式开启：

```bash
make bench                            # JSON 2 万行 / xlsx 1 万行
DTOOL_BENCH_ROWS=200000 make bench    # 放大：JSON 20 万行 / xlsx 20 万行
go test ./internal/query -bench LoadStream -benchmem   # 只看某一项
```

## 内存不足时的行为

为避免「进程被内核静默杀掉、用户不知道发生了什么」，dtool 会：

- **载入前预估**：按所选装入方式的倍率推算峰值内存，超出可用预算时直接失败，并给出数字与处置建议。建议分场景给：`query` 会推荐 `--load-mode stream`，`convert`（Excel 无流式开关）只推荐拆分输入，两者都保留 `--max-memory 0` 强制运行口；
- **运行期看门狗**：载入/查询期间监控本进程内存，逼近预算时以普通错误中止，并写入一条 `failed` 的 Action；
- **载入进度**：数据源 ≥8MB 时向 stderr 打印「载入 xx（大小，装入方式，预计需约 xx 内存）...」，即使进程被强杀也能看出卡在哪里。

自动探测 cgroup v2/v1 内存限制与系统可用内存（macOS/Windows 上需显式指定）。可用全局参数覆盖：

```bash
dtool query --max-memory 4G --sql '...'   # 显式预算
dtool query --max-memory 0  --sql '...'   # 关闭检查（内存不足时仍会被系统杀掉）
DTOOL_MAX_MEMORY=2G dtool pipeline ...    # 环境变量，适合容器/CI
```

配置文件同样支持：

```yaml
load_mode: auto      # auto / stream / full
```

## 检查更新与升级

```bash
dtool --update [--pre]                      # 只检查是否有新版本（--pre 包含预览版）
dtool upgrade [--pre]                       # 升级到最新版本
dtool upgrade --version 1.2.0               # 指定版本（可降级）；预览版如 1.2.0-preview.1
```

升级前会校验 Release 中 `checksums.txt` 的 sha256，并先运行新二进制自检；任何一步失败都不会改动现有文件。Windows 下运行中的 exe 不能覆盖，因此先把旧文件改名为 `.old` 再让新文件就位（失败回滚，`.old` 下次启动自动清理）。GitHub 限流时设置 `GITHUB_TOKEN`；`DTOOL_REPO` / `DTOOL_UPDATE_API` 可指向镜像。

面向 AI Agent 的使用指南见 [skills.md](skills.md)。

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

## 许可证

dtool 以 [MIT](LICENSE) 协议发布。依赖均为宽松协议（MIT / BSD-3-Clause / Apache-2.0；`golang/freetype` 双协议中选用 FreeType License），不含 GPL/LGPL/MPL 代码。

第三方版权声明与许可证全文（`THIRD_PARTY_NOTICES.md`）在每次打包时由 `scripts/gen-notices.sh` 根据当前依赖自动生成，并随发布压缩包分发；它不提交到仓库，升级依赖无需额外操作。需要时可运行 `make notices` 本地生成。
