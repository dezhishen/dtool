#!/usr/bin/env bash
# 解析并校验发版 tag，输出 KEY=VALUE（可直接追加到 $GITHUB_OUTPUT）。
# 规则：
#   预览版  vX.Y.0-preview.N   （仅允许针对 X.Y.0）
#   正式版  vX.Y.0
#   补丁版  vX.Y.Z（Z>=1，基于对应正式版）
# 用法: release-info.sh <tag>
set -euo pipefail

tag="${1:?usage: release-info.sh <tag>}"
re='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-preview\.([1-9][0-9]*))?$'
if [[ ! "$tag" =~ $re ]]; then
  echo "invalid tag '$tag': want vX.Y.0-preview.N | vX.Y.0 | vX.Y.Z" >&2
  exit 1
fi
major="${BASH_REMATCH[1]}" minor="${BASH_REMATCH[2]}" patch="${BASH_REMATCH[3]}" pre="${BASH_REMATCH[4]}"

if [[ -n "$pre" ]]; then
  if [[ "$patch" != 0 ]]; then
    echo "preview tags are only allowed for vX.Y.0, got '$tag'" >&2
    exit 1
  fi
  kind=preview
elif [[ "$patch" == 0 ]]; then
  kind=stable
else
  kind=patch
  base="v${major}.${minor}.0"
  if ! git rev-parse -q --verify "refs/tags/${base}" >/dev/null; then
    echo "patch release '$tag' requires stable tag '$base' to exist" >&2
    exit 1
  fi
fi

# 上一个对比基线：预览版取最近的任意 tag；正式版/补丁版取最近的非预览 tag，
# 因此正式版的说明会汇总整个预览期的改动。
#
# 两种 tag 必须排除：
#   dev / dev-*  ——滚动 dev 发布每次构建都移动，比任何版本 tag 都近；不排除的话
#                   prev 会变成移动的 "dev"，发版说明既没意义也不可复现（实测复现过）
#   *-preview.*  ——正式版/补丁版要汇总整个预览期，基线应落到上一个正式版
prev=""
exclude=(--exclude 'dev' --exclude 'dev-*')
if [[ "$kind" == preview ]]; then
  prev="$(git describe --tags --abbrev=0 "${exclude[@]}" "${tag}^" 2>/dev/null || true)"
else
  prev="$(git describe --tags --abbrev=0 "${exclude[@]}" --exclude '*-preview.*' "${tag}^" 2>/dev/null || true)"
fi

echo "kind=${kind}"
echo "prev=${prev}"
echo "version=${tag#v}"
if [[ "$kind" == preview ]]; then echo "prerelease=true"; else echo "prerelease=false"; fi
