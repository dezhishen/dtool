#!/usr/bin/env bash
# doc-flags：文档与 CLI 的开关必须双向同步
#
# 为什么需要它：文档里写一个 CLI 不认识的开关（用户会照抄 → 报错），或者 CLI 新加了开关
# 却没写进 Design.md（AI/用户不知道它存在、更不会用），都只能靠人眼发现。这里把两条
# 方向都变成断言：
#   1) 文档里出现的每个 --flag 必须真实存在（白名单放行外部工具 git/gh 的开关）；
#   2) CLI 的每个开关（除内置 --help）必须写进 Design.md 的参考表。
# 注意：只比对「长开关名」，短开关（-c / -h）不参与，避免与命令行里的普通短横线混淆。
set -euo pipefail
cd "$(dirname "$0")/../.."

# 文档里允许出现、但不属于 dtool 的开关（git / gh 等外部工具）
EXTERNAL_FLAGS=(
  --always --clobber --dry-run --exclude --ignore-missing --pid
  --retention-days --scope --since --user --repo --json --jq --limit
)
# CLI 的内置开关（cobra 自带，不需要写进参考文档）
BUILTIN_FLAGS=(--help)

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail=0

go build -o "$tmp/dtool" . >/dev/null

# 1) 收集 CLI 的全部开关：根命令 + 每个子命令 + 子命令的子命令（递归两层，够用）
{
  "$tmp/dtool" --help 2>&1 || true
  for c in $("$tmp/dtool" --help 2>&1 | awk '/^Available Commands:/,/^$/ {print $1}' |
    grep -vE '^(Available|Commands:|completion|help)$'); do
    "$tmp/dtool" "$c" --help 2>&1 || true
    for s in $("$tmp/dtool" "$c" --help 2>&1 | awk '/^Available Commands:/,/^$/ {print $1}' |
      grep -vE '^(Available|Commands:)$'); do
      "$tmp/dtool" "$c" "$s" --help 2>&1 || true
    done
  done
} | grep -ohE -- '--[a-z][a-z0-9-]*' | sort -u >"$tmp/cli.txt"

# 2) 收集文档里出现的开关（README / skills / Design / docs / examples）
grep -rohE -- '--[a-z][a-z0-9-]*' README.md Design.md docs examples skills 2>/dev/null |
  sort -u >"$tmp/doc.txt"

[[ -s "$tmp/cli.txt" ]] || { echo "FAIL: 没能从 --help 里解析出任何开关" >&2; exit 1; }
[[ -s "$tmp/doc.txt" ]] || { echo "FAIL: 没能从文档里解析出任何开关" >&2; exit 1; }

# 方向一：文档 → CLI
while read -r f; do
  [[ -n "$f" ]] || continue
  if ! grep -qx -- "$f" "$tmp/cli.txt"; then
    if printf '%s\n' "${EXTERNAL_FLAGS[@]}" | grep -qx -- "$f"; then
      continue
    fi
    echo "FAIL: 文档提到了 CLI 没有的开关 $f（要么删文档，要么加进 EXTERNAL_FLAGS 白名单）" >&2
    fail=1
  fi
done <"$tmp/doc.txt"

# 方向二：CLI → Design.md（参考文档必须覆盖每个开关）
grep -rohE -- '--[a-z][a-z0-9-]*' Design.md | sort -u >"$tmp/design.txt"
while read -r f; do
  [[ -n "$f" ]] || continue
  printf '%s\n' "${BUILTIN_FLAGS[@]}" | grep -qx -- "$f" && continue
  if ! grep -qx -- "$f" "$tmp/design.txt"; then
    echo "FAIL: CLI 有开关 $f，但 Design.md 没写（参考文档必须覆盖全部开关）" >&2
    fail=1
  fi
done <"$tmp/cli.txt"

# 方向三：关键开关的取值也要在文档里写全（取值错了同样是漂移）
# --load-mode / --store / --mem-policy / --channel 是操作者强指定执行方式的入口
for pair in "load-mode:stream" "load-mode:full" "store:memory" "store:disk" \
  "mem-policy:try" "mem-policy:strict" "channel:preview" "channel:dev"; do
  flag="${pair%%:*}"; value="${pair#*:}"
  "$tmp/dtool" --help 2>&1 | grep -q -- "--$flag" || continue
  if ! grep -q -- "$value" Design.md; then
    echo "FAIL: Design.md 没写 --$flag 的取值 $value" >&2
    fail=1
  fi
done

if [[ "$fail" -eq 0 ]]; then
  echo "ok: 文档与 CLI 的开关双向同步（CLI $(wc -l <"$tmp/cli.txt") 个，文档 $(wc -l <"$tmp/doc.txt") 个）"
fi
exit "$fail"
