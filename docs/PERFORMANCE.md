# 性能测试报告

记录 dtool 的性能测试**环境、方式与结果**，以及 1 核 2GB 下的能力边界。
README 只保留结论与入口，细节以本文为准。

- 测试日期：2026-10-09
- 被测版本：Go 1.27.1 / linux-amd64；「旧版」= v0.1.0 二进制（Excel 整表物化实现），
  「新版」= 提交 `2f65679` + 流式转换改造

## 1. 结论速览（1 核 2GB）

| 场景 | 结论 |
|------|------|
| `convert`（Excel 转换） | 峰值内存 ≈ **32MB + 文件 × 3**，与行数无关；Excel 单表上限（1,048,576 行）内**碰不到内存墙**：53.3MB / 105 万行 × 8 列只要 **72MB**、**1:10** |
| `query`（JSON 查询，默认 `auto` → `stream`） | 峰值内存 ≈ **文件 × 1.2–1.3**；默认预检下文件 ≤ **~850MB**（预检按 2× 文件估算，超可用 1.7GB 即拦下） |
| `--max-memory 0`（关闭预检） | 实测 **1.34GB** 文件可跑完（**1637MB**、3:42）；1.77GB 文件未实测，按 1.3× 外推 ≈2.3GB，会越过 2GB 硬上限 |
| 内存不足时 | **不静默被杀**：预检在 0.01s 内以 rc=4 返回「需 xx / 可用 xx」与退出口（`query`：`--load-mode stream`；`convert`：缩小输入；两者：`--max-memory 0`）；超预算时看门狗先打印原因再中止 |

## 2. 测试环境

| 项 | 值 |
|----|-----|
| 机器 | x86_64，4 核（`nproc`），物理内存 9.7GB |
| 资源限制 | `systemd-run --user --scope -p MemoryMax=2G -p MemorySwapMax=0 -p CPUQuota=100%` + `taskset -c 0` → **1 核 + cgroup v2 2GB 硬限制 + 禁 swap** |
| 峰值内存口径 | `/usr/bin/time -v` 的 `Maximum resident set size`（内核高水位 RSS，与 cgroup OOM 判定同一口径） |
| 被测二进制 | `dtool`（旧版，v0.1.0）/ `dtool_new`（新版，工作区构建） |
| 磁盘 | 测试期占用约 6.5GB 大文件，`/` 余量 24GB |

**为什么用 RSS 而不是 Go 的 `HeapAlloc`**：`HeapAlloc` 采样会包含尚未回收的垃圾，且看不到
非 Go 堆的分配（modernc sqlite 的页、excelize 落盘的 worksheet 数据），与 OOM 判定不一致。
只有 VmHWM 能在「被 OOM 杀掉」和「安全跑完」之间给出可比较的结论。

## 3. 测试方式（可复现）

### 3.1 端到端（受限环境）

```bash
cat > /tmp/bench.sh <<'SH'
#!/usr/bin/env bash
# 在 1 核 + 2GB 内存（cgroup v2 硬限制）下运行一条命令，输出耗时与峰值 RSS。
set -uo pipefail
MEM="${MEM:-2G}"
run() { # run <label> <cmd...>
  local label="$1"; shift
  local out rc wall rss result
  out="$(systemd-run --user --scope -q -p MemoryMax="$MEM" -p MemorySwapMax=0 -p CPUQuota=100% \
        -- taskset -c 0 /usr/bin/time -v "$@" 2>&1)"
  rc=$?
  wall="$(grep -oP 'Elapsed \(wall clock\) time.*: \K.*' <<<"$out" || echo -)"
  rss="$(grep -oP 'Maximum resident set size \(kbytes\): \K\d+' <<<"$out" || echo 0)"
  if   [[ $rc -eq 0 ]];   then result=OK
  elif [[ $rc -eq 124 ]]; then result=TIMEOUT
  elif [[ $rc -eq 143 ]]; then result="OOM/被SIGTERM(143)"
  else result="FAIL(rc=$rc)"; fi
  printf '%-34s %-18s %-12s %8s MB\n' "$label" "$result" "$wall" "$((rss/1024))"
  [[ "${VERBOSE:-0}" == 1 ]] && grep -E "fatal error|out of memory|panic" <<<"$out" | head -3
}
SH
source /tmp/bench.sh

./dtool version                                   # 记录版本
run "xlsx 7.4MB / 15 万行" ./dtool convert --input /tmp/orders_150000.xlsx --name big
run "query JSON 456MB"     ./dtool query --source d=/tmp/k6_5m.json --sql "SELECT COUNT(*) FROM d"
```

### 3.2 自动化回归（随 `go test ./...` 跑，`-short` 跳过）

```bash
go test ./internal/query ./internal/pipeline -run Perf -v
```

| 用例 | 断言 | 采样口径 |
|------|------|----------|
| `TestPerfLoadModeMemoryRatio` | 流式装入的**存活堆**峰值比整块解析低 1.3 倍以上 | `perftest.Live`：每 10ms 停一次世界，取 GC 后的存活集 |
| `TestPerfConvertExcelMemory` | Excel 转换的**峰值堆**不得超过 `xlsxNeed(文件) × 1.3` | `perftest.Inclusive`：直接读 `HeapAlloc`（上界断言，宁可保守） |

### 3.3 基准

```bash
make bench                                              # JSON 2 万行 / xlsx 4 万行
DTOOL_BENCH_ROWS=200000 make bench                      # 放大行数
go test -run '^$' -bench LoadStream -benchmem ./internal/query/
```

CI 的 `lint` job 会额外跑一轮 `-bench . -benchtime 1x` 冒烟：只确认基准可用，
不做耗时/内存断言（避免 runner 抖动误报）。

### 3.4 测试数据

- xlsx：`examples/tools/genxlsx`（仓库内，生成示例数据）；极限用例用临时 StreamWriter
  生成器（指定行数 × 列数，`excelize.NewStreamWriter`，1M 行约 1 分钟）。
- JSON：按目标体积**拼接同结构数组**（剥离每段的 `[`/`]` 后串联），避免慢速逐行生成；
  各规模文件均由 `dtool` 自身可读的真实结构数据拼成。

## 4. 结果

### 4.1 Excel 转换（`convert`）：改造前 vs 改造后

| 输入 | 改造前（整表物化） | 改造后（两阶段流式） |
|------|--------------------|----------------------|
| 13KB / 3 行（示例 sales） | 0.04s，19.5MB | 0.03s，**18MB** |
| 7.4MB / 150,001 行 × 9 列 | 22.4s，**1942MB** | 16.1s（另一次 12.8s），**43MB** |
| 17.8MB / 400,001 行 × 6 列 | **被 cgroup OOM 杀掉**（rc=143，日志停在「预计需约 4.3GB」） | 26.8s，**72MB** |
| 53.3MB / 1,048,575 数据行 × 8 列（Excel 行数上限） | 未测（必然 OOM） | **1:09.6，72MB** |
| 预算不足：17.8MB + `--max-memory 100M` | — | 0.01s、14MB 内被拦（rc=4，报「需 134MB」） |

内存降约 **45×**，耗时同时下降约 30%（省掉了物化与整块序列化）。
**产物一致性**：`orders_150000`、`sales`、`hr`、`messy` 四个文件的 `data.json` 与改造前
**逐字节一致**，Schema 除 `updated_at` 外一致，告警（合并单元格/空表头/重复表头）逐条一致。

### 4.2 JSON 查询（`query`，1 核 2GB）

| 输入 | 结果 | 说明 |
|------|------|------|
| 17MB / 10 万行 | 3.2s，236MB | 走 full |
| 104MB / 100 万行日志 | 22.5s，154MB | 走 stream |
| 168MB / 100 万行订单 | 29.6s，182MB | 走 stream |
| 456MB / 500 万行 | 1:31，603MB | 走 stream |
| 1.08GB / 970 万行 | 3:03，1403MB | `--max-memory 0` |
| 1.34GB / 1400 万行 | 3:42，1637MB | `--max-memory 0` |
| 1.34GB（默认预检） | 0.01s 被拦（rc=4，需 2.67GB） | 预检按 2× 文件 |
| 168MB + `--load-mode full` | 0.01s 被拦（rc=4） | 整块解析需 13× 文件 |

历史对照（JSON 只有整块解析时）：104MB → 16.5s / 1543MB；168MB → 被 OOM 杀掉；
456MB → 被 OOM 杀掉。

### 4.3 能力边界（1 核 2GB）

预检公式（`internal/memguard` + `internal/pipeline`）：

```
可用内存 = cgroup 限制（或 --max-memory）× 85%        # 1H2G → 约 1.7GB
query:  need = Σ 文件大小 × 峰值因子（full 13 / stream 2）
xlsx:   need = 32MB（固定开销）+ 文件大小 × 6
need > 可用内存 → 转换/查询前直接以 rc=4 失败并给出数字
```

| 输入 | 默认预检 | 关闭预检（`--max-memory 0`） |
|------|----------|------------------------------|
| JSON ≤ 850MB | 放行 | 放行（实测 456MB → 603MB） |
| JSON 1.08GB | **拦下**（需 2.0GB > 1.7GB） | 放行：3:03，1403MB |
| JSON 1.34GB | **拦下**（需 2.67GB） | 放行：3:42，1637MB |
| JSON 1.77GB | **拦下** | 未实测；按 1.3× 外推 ≈2.3GB，预计被 OOM 杀掉 |
| xlsx ≤ 4MB | 放行 | — |
| xlsx 53MB（105 万行） | 放行（需 352MB） | 实测 72MB |

要点：
- **xlsx 侧的瓶颈不是内存**，而是 Excel 格式本身（单表 1,048,576 行 × 16,384 列）。
- **JSON 侧的瓶颈是内存**：1H2G 下实测上限约 1.34GB（≈1.6GB RSS），再大就会被内核杀掉——
  所以默认预检取流式 2× 的保守倍率，把「会死」的情况变成「提前报错 + 给退出口」。

### 4.4 基准（4 核不限资源，`-benchtime 1s`）

| 基准 | ns/op | rows/s | MB/s | B/op | allocs/op |
|------|-------|--------|------|------|-----------|
| `BenchmarkLoadFullCount`（JSON 2 万行） | 260.9ms | 76,653 | 5.76 | 43.6MB | 1,140,025 |
| `BenchmarkLoadStreamCount` | 377.5ms | 52,986 | 3.98 | 42.9MB | 1,540,177 |
| `BenchmarkLoadFullGroupBy` | 285.4ms | 70,075 | 5.26 | 43.6MB | 1,140,074 |
| `BenchmarkLoadStreamGroupBy` | 378.8ms | 52,803 | 3.97 | 43.0MB | 1,540,207 |
| `BenchmarkConvertExcel`（xlsx 4 万行 / 1.4MB） | 2.08s | 19,255 | 0.71 | 412MB | 9,688,959 |
| `BenchmarkResolveMode`（自适应判断） | 21.5ns | — | — | 0 | 0 |

`stream` 比 `full` 慢约 30%（解析两遍），换来 10–40× 的内存优势；xlsx 转换的 `B/op`
较高来自 excelize 的单元格格式化开销（累计分配），但**驻留**内存只有几十 MB。

### 4.5 回归用例实测

| 用例 | 实测 | 阈值 |
|------|------|------|
| `TestPerfLoadModeMemoryRatio`（6 万行 / 4.3MB） | full 存活堆 30.1MB（×6.9），stream 3.2MB（×0.7） | full ≥ stream × 1.3 |
| `TestPerfConvertExcelMemory`（4 万行 / 1.4MB） | 峰值堆 37.3MB | ≤ 预检估算 40.4MB × 1.3 |

## 5. 已知取舍

- `stream` 两遍解析：CPU 换内存，大文件默认走它（`auto`：≥32MB 或整块解析超预算时切换）。
- Excel 转换会落一份临时 JSONL（与 `data.json` 同量级），随 staging 目录一起清理；
  换来的是「单次解析 + 常量内存」。
- Excel 合并单元格数量改为流式扫 worksheet XML（`internal/converter/merge.go`），
  不再用 `GetMergeCells`（后者会把整表反序列化，实测 15 万行 × 9 列多花 855MB）。
- 预检是**保守**估计：宁可在大文件上提前报错（附 `--max-memory 0` 等退出口），
  也不要让用户看到进程被内核杀掉。
- `auto` 给整块解析留 20% 余量：预计峰值 > 预算的 80% 时改走 `stream`（擦着预算通过往往意味着
  中途被看门狗中止）；显式 `--load-mode full` 不受此限，但逼近预算时会先打印提示。
- 整块解析改为**逐元素解码**（`internal/query/load.go:decodeRows`）：不再同时持有「文件原始字节
  + 解码后的行」，15MB 文件峰值 169MB（此前同预算下会被中止）；每 4096 行检查一次 ctx，
  内存超预算时能及时中止。
- 用量口径按**工作集/RSS**，不是 Go 堆：modernc/SQLite 的页缓存是 mmap/VirtualAlloc 出来的，
  `debug.SetMemoryLimit` 管不到它，只有预检 + 看门狗能提前拦住。
- Windows 的用量口径是**私有提交量**（commit charge，`GetProcessMemoryInfo` 的 `PagefileUsage`）而不是
  工作集：`JOB_OBJECT_LIMIT_PROCESS_MEMORY` 限的就是提交量，按工作集看会等到提交顶满才报错。
- 平台探测：Linux 用 cgroup v2/v1 + 系统可用内存；Windows 用 Job Object 进程内存上限 + 系统可用内存；
  其他平台需显式 `--max-memory`。预算为零等于不做检查，此时顶到硬上限的表现可能是结构化错误
  （SQLite `out of memory (7)`，会被翻译成带 hint 的 code 4），也可能是 Go runtime 的
  `fatal error: out of memory`（打印堆栈、退出码 2，无法恢复）。
