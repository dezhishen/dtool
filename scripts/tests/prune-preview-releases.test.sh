#!/usr/bin/env bash
# prune-preview-releases.sh 的选择逻辑单测：只验证「该删哪个」，不碰网络与 gh。
set -euo pipefail
cd "$(dirname "$0")/../.."
# shellcheck source=../prune-preview-releases.sh
source scripts/prune-preview-releases.sh --source-only

now=1700000000
day=86400
fail() { echo "prune-preview-releases: $*" >&2; exit 1; }

check() { # check <说明> <期望> <保留天数> <fixture...>
  local desc="$1" want="$2" days="$3"
  shift 3
  local got
  got="$(printf '%s\n' "$@" | select_prune "$days" "$now")"
  [[ "$got" == "$want" ]] || fail "$desc：得到 [$got]，期望 [$want]"
}

# 正式版已存在：到期（30 天前发布）要删，没到期（15 天前…）也删——
# 16 天前发布正式版即满足 14 天保留期，这里用 15 天造一个「刚够」的样例。
check "正式版已发布且超过保留期" \
  "$(printf 'v0.1.0-preview.1')" 14 \
  "$(printf 'v0.1.0-preview.1\tv0.1.0\t%s' "$((now - 30 * day))")" \
  "$(printf 'v0.1.0-preview.2\tv0.1.0\t%s' "$((now - 13 * day))")"

# 正式版还没有（stable 列为空、epoch 为 0）：不能删
check "正式版尚未发布" "" 14 \
  "$(printf 'v0.9.0-preview.1\t\t0')"

# 保留期边界：正好 14 天删，差 1 秒不删
check "正好到期" "$(printf 'v0.1.0-preview.9')" 14 \
  "$(printf 'v0.1.0-preview.9\tv0.1.0\t%s' "$((now - 14 * day))")"
check "差一秒不到期" "" 14 \
  "$(printf 'v0.1.0-preview.9\tv0.1.0\t%s' "$((now - 14 * day + 1))")"

# 保留期可配置：0 天表示正式版一发就清理
check "保留期 0 天" "$(printf 'v0.2.0-preview.1')" 0 \
  "$(printf 'v0.2.0-preview.1\tv0.2.0\t%s' "$now")"

# 只按正式版发布日起算：预览版本身很旧、正式版刚发布也不能删
check "预览版旧但正式版刚发布" "" 14 \
  "$(printf 'v0.2.0-preview.1\tv0.2.0\t%s' "$((now - 3600))")"

echo "prune-preview-releases: OK"
