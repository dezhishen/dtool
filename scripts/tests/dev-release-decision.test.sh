#!/usr/bin/env bash
# dev-release-decision.sh：commit 未变就别重复发布
set -euo pipefail
cd "$(dirname "$0")/../.."

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail=0

check() { # check <描述> <期望 release=值> <期望 reason 片段> <提交> [dev-build.txt 路径|--stdin 内容]
  local desc="$1" want="$2" fragment="$3" commit="$4"; shift 4
  local out
  if [[ "${1:-}" == "--stdin" ]]; then
    out="$(printf '%s' "$2" | bash scripts/dev-release-decision.sh "$commit")"
  elif [[ -n "${1:-}" ]]; then
    out="$(bash scripts/dev-release-decision.sh "$commit" "$1")"
  else
    out="$(bash scripts/dev-release-decision.sh "$commit" /nonexistent/dev-build.txt)"
  fi
  if ! grep -q "^release=${want}$" <<<"$out"; then
    echo "FAIL: $desc → 期望 release=$want，实际：$(tr '\n' ' ' <<<"$out")" >&2; fail=1
  fi
  if [[ -n "$fragment" ]] && ! grep -q "$fragment" <<<"$out"; then
    echo "FAIL: $desc → reason 缺少 '$fragment'，实际：$(tr '\n' ' ' <<<"$out")" >&2; fail=1
  fi
}

good="dev-38046361651
bd19d46bf2e25fdc95d704590e916f98e2a6157d
2026-10-10T10:56:30Z
https://github.com/dezhishen/dtool/actions/runs/38046361651"
printf '%s\n' "$good" > "$tmp/dev-build.txt"

# 1) commit 相同 → 跳过（这是定时/手动触发最常见的路径）
check "commit 相同" false "main 未变" bd19d46bf2e25fdc95d704590e916f98e2a6157d "$tmp/dev-build.txt"
# 2) 大小写不同也应视为相同（GitHub 给的是小写，但别依赖这点）
check "大小写不同" false "" BD19D46BF2E25FDC95D704590E916F98E2A6157D "$tmp/dev-build.txt"
# 3) 多行里的第 1 行是构建号，不能被误当成 commit
check "取第 2 行" true "" dev-38046361651 "$tmp/dev-build.txt"
# 4) commit 变了 → 需要发布
check "commit 变化" true "有新提交" 1111111111111111111111111111111111111111 "$tmp/dev-build.txt"
# 5) 首次发布（没有文件 / 文件为空 / commit 记 unknown）都要发布
check "无 dev-build.txt" true "首次发布" abc123 /nonexistent/dev-build.txt
printf '' > "$tmp/empty.txt"
check "空文件" true "" abc123 "$tmp/empty.txt"
printf 'dev-1\nunknown\n' > "$tmp/unknown.txt"
check "commit 记 unknown" true "" abc123 "$tmp/unknown.txt"
# 6) --force 忽略校验（手动触发的「就要重发一次」）
out="$(bash scripts/dev-release-decision.sh bd19d46bf2e25fdc95d704590e916f98e2a6157d "$tmp/dev-build.txt" --force)"
grep -q '^release=true$' <<<"$out" || { echo "FAIL: --force 应强制发布" >&2; fail=1; }
# 7) stdin 模式与文件模式等价（workflow 里可能用管道）
check "stdin 模式" false "" bd19d46bf2e25fdc95d704590e916f98e2a6157d --stdin "$good"
# 8) CRLF 行尾不该导致误判
printf 'dev-1\r\nbd19d46bf2e25fdc95d704590e916f98e2a6157d\r\n' > "$tmp/crlf.txt"
check "CRLF 行尾" false "" bd19d46bf2e25fdc95d704590e916f98e2a6157d "$tmp/crlf.txt"

if (( fail )); then echo "dev-release-decision: FAILED" >&2; exit 1; fi
echo "dev-release-decision: OK"
