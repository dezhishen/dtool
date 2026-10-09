#!/usr/bin/env bash
# 生成 Release 说明：汇总上一个基线 tag 到当前 tag 之间的 PR 与 commits。
# 用法: release-notes.sh <tag> <prev-tag|""> [repo]   （需要 gh CLI 与 GH_TOKEN；无 gh 时仅输出 commits）
set -euo pipefail

tag="${1:?usage: release-notes.sh <tag> <prev-tag> [repo]}"
prev="${2:-}"
repo="${3:-${GITHUB_REPOSITORY:-dezhishen/dtool}}"

if [[ -n "$prev" ]]; then range="${prev}..${tag}"; else range="$tag"; fi

echo "## 变更说明"
echo
if [[ -n "$prev" ]]; then
  echo "对比基线：[\`${prev}\`](https://github.com/${repo}/releases/tag/${prev}) → \`${tag}\`"
else
  echo "首次发布。"
fi
echo

echo "### Pull Requests"
echo
prs=""
if command -v gh >/dev/null 2>&1 && [[ -n "${GH_TOKEN:-${GITHUB_TOKEN:-}}" ]]; then
  args=(-f "tag_name=${tag}")
  [[ -n "$prev" ]] && args+=(-f "previous_tag_name=${prev}")
  prs="$(gh api -X POST "repos/${repo}/releases/generate-notes" "${args[@]}" --jq .body 2>/dev/null || true)"
fi
if [[ -n "$prs" ]]; then
  # 去掉自动生成的标题，避免重复
  printf '%s\n' "$prs" | sed -E '/^## What.s Changed$/d'
else
  echo "_无可用的 PR 信息_"
fi
echo

echo "### Commits"
echo
n="$(git rev-list --no-merges --count "$range")"
if [[ "$n" == 0 ]]; then
  echo "_无新的 commit_"
else
  git log --no-merges --pretty=format:"- %s ([\`%h\`](https://github.com/${repo}/commit/%H)) — %an" "$range"
  echo
fi
