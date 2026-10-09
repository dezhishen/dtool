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
