#!/usr/bin/env bash
# 判断「这次触发是否需要重新发布 dev」。
#
# dev 是滚动发布（固定 tag dev，资产每次覆盖），触发来源有三：push main、每 4 小时定时、
# 手动触发。定时与手动都可能落在「main 没有新提交」的时刻——那时重建再覆盖一遍纯属浪费，
# 而且会让 dev 的构建号无意义地滚动。所以用 dev 发布里的 dev-build.txt（第 2 行是 commit）
# 与当前提交比对：相同就跳过，除非显式 --force。
#
# 用法: dev-release-decision.sh <当前 commit> [dev-build.txt 路径] [--force]
#       未给文件路径时从 stdin 读
# 输出（可直接追加到 $GITHUB_OUTPUT）:
#   release=true|false
#   reason=<一句话说明>
set -euo pipefail

commit="${1:?usage: dev-release-decision.sh <commit> [dev-build.txt] [--force]}"
shift
file=""
force=0
for arg in "$@"; do
  case "$arg" in
    --force) force=1 ;;
    *) file="$arg" ;;
  esac
done

if [[ -n "$file" ]]; then
  if [[ -f "$file" ]]; then content="$(cat "$file")"; else content=""; fi
else
  content="$(cat || true)"
fi

# 第 2 行 = commit（第 1 行是构建号）；unknown/空都视为「无法判定」
recorded="$(printf '%s' "$content" | sed -n '2p' | tr -d '\r' | tr -d ' ' | tr '[:upper:]' '[:lower:]')"
current="$(printf '%s' "$commit" | tr -d '\r' | tr -d ' ' | tr '[:upper:]' '[:lower:]')"

if [[ $force -eq 1 ]]; then
  echo "release=true"
  echo "reason=--force：忽略 commit 校验，强制重新发布"
  exit 0
fi
if [[ -z "$recorded" || "$recorded" == "unknown" ]]; then
  echo "release=true"
  echo "reason=没有可用的历史记录（首次发布或 dev-build.txt 缺失），需要发布"
  exit 0
fi
if [[ "$recorded" == "$current" ]]; then
  echo "release=false"
  echo "reason=main 未变（commit $current 与 dev 发布一致），跳过重复构建"
  exit 0
fi
echo "release=true"
echo "reason=main 有新提交（$recorded → $current），需要重新发布"
