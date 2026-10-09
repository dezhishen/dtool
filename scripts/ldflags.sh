#!/usr/bin/env bash
# 输出 go build 的 -ldflags 字符串：把版本与构建元数据嵌入二进制（Makefile 与 build-release.sh 共用）。
# 元数据优先取环境变量，其次 GitHub Actions 自带变量，最后回退 git/本机时间。
# 用法: ldflags.sh <version>
set -euo pipefail

version="${1:?usage: ldflags.sh <version>}"
pkg="github.com/dezhishen/dtool/internal/buildinfo"

# 仅在命令成功时输出（无提交的仓库里 rev-parse 会失败却回显 HEAD）
git_out() { local o; o="$(git "$@" 2>/dev/null)" && printf '%s' "$o" || true; }

commit="${COMMIT:-${GITHUB_SHA:-$(git_out rev-parse HEAD)}}"
[[ "$commit" =~ ^[0-9a-f]{7,40}$ ]] || commit=""
commit_date="${COMMIT_DATE:-$(git_out log -1 --format=%cI "${commit:-HEAD}")}"
if [[ -z "${BRANCH:-}" && "${GITHUB_REF_TYPE:-}" == tag ]]; then branch=""; # 发版 tag 构建没有分支
else branch="${BRANCH:-${GITHUB_REF_NAME:-$(git_out rev-parse --abbrev-ref HEAD)}}"; fi
[[ "$branch" == HEAD ]] && branch="" # detached HEAD
build_date="${BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
build_id="${BUILD_ID:-${GITHUB_RUN_ID:-}}"
build_url="${BUILD_URL:-}"
if [[ -z "$build_url" && -n "${GITHUB_RUN_ID:-}" && -n "${GITHUB_REPOSITORY:-}" ]]; then
  build_url="${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY}/actions/runs/${GITHUB_RUN_ID}"
fi
builder="${BUILDER:-$([[ "${GITHUB_ACTIONS:-}" == true ]] && echo github-actions || echo local)}"
dirty="${DIRTY:-}"
if [[ -z "$dirty" && -n "$(git_out status --porcelain)" ]]; then dirty=true; fi

flags=(-s -w)
add() { [[ -n "$2" ]] && flags+=(-X "${pkg}.$1=${2// /_}") || true; }
add Version "$version"; add Commit "$commit"; add CommitDate "$commit_date"; add Branch "$branch"
add Dirty "$dirty"; add BuildDate "$build_date"; add BuildID "$build_id"; add BuildURL "$build_url"; add Builder "$builder"
echo "${flags[*]}"
