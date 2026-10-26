#!/usr/bin/env bash
# gen-notices.sh 的单测：THIRD_PARTY_NOTICES.md 的内容必须全部从当前依赖推导出来。
# 覆盖：可复现（与 locale 无关）、模块段齐全、概览表与模块一一对应、
# 许可证类型真的被识别出来（不是一片"未识别"）、双许可与内嵌资源条目被校验通过。
set -euo pipefail
cd "$(dirname "$0")/../.."

fail() { echo "gen-notices: $*" >&2; exit 1; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

bash scripts/gen-notices.sh "$tmp/a.md" >/dev/null
LC_ALL=zh_CN.UTF-8 bash scripts/gen-notices.sh "$tmp/b.md" >/dev/null
cmp -s "$tmp/a.md" "$tmp/b.md" ||
  fail "不同 locale 下生成结果不一致（排序未固定为 LC_ALL=C？）"

# CGO=1 时 DuckDB 与 SQLite 一定被链接，清单里必须有它们
for m in github.com/duckdb/duckdb-go/v2 github.com/duckdb/duckdb-go-bindings modernc.org/sqlite; do
  grep -qE "^## ${m} v" "$tmp/a.md" || fail "缺少模块段：$m"
done

# 概览表行数必须与模块段数一致（一个模块一行，不多不少）
mods="$(grep -cE '^## [^ ]+ v[0-9]' "$tmp/a.md" || true)"
rows="$(grep -cE '^\| `' "$tmp/a.md" || true)"
[[ "$mods" -gt 20 ]] || fail "模块段异常少：$mods"
[[ "$mods" == "$rows" ]] || fail "概览表行数($rows) != 模块段数($mods)"

# 许可证类型必须被识别（出现"未识别"说明识别规则覆盖不足）
grep -q '未识别' "$tmp/a.md" && fail "存在未识别的许可证类型，需要补充识别规则"

# 抽查两个已知许可：cobra = Apache-2.0、excelize = BSD-3-Clause
grep -qE '^\| `github.com/spf13/cobra` .*\| Apache-2\.0 \|$' "$tmp/a.md" ||
  fail "cobra 未被识别为 Apache-2.0"
grep -qE '^\| `github.com/xuri/excelize/v2` .*\| BSD-3-Clause \|$' "$tmp/a.md" ||
  fail "excelize 未被识别为 BSD-3-Clause"

# 自动推导的三个结论段：类型分布 / 需留意 / 双许可 / 内嵌资源
grep -q '^- 许可证类型分布：' "$tmp/a.md" || fail "缺少许可证类型分布"
grep -q '^- 需留意' "$tmp/a.md" || fail "缺少需留意清单"
grep -q '双许可取舍：`github.com/golang/freetype`' "$tmp/a.md" ||
  fail "缺少双许可取舍条目（freetype 应选用 FTL）"
grep -qE '^\| `github.com/golang/freetype` .*\| FTL \|$' "$tmp/a.md" ||
  fail "freetype 的生效许可证应为 FTL"
grep -q '内联了 Roboto-Medium.ttf' "$tmp/a.md" || fail "缺少内嵌资源条目"

# 正文里必须真的收录许可证全文（不只是概览）
grep -q '~~~~text' "$tmp/a.md" || fail "没有收录任何许可证全文"

echo "gen-notices: OK"
