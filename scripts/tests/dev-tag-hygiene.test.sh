#!/usr/bin/env bash
# 滚动 dev tag 不得污染其他渠道：它是**移动** tag，每次都落在 main 最新提交上，
# 而 `git describe` 挑最近的 tag —— 不排除的话会污染
#   ① 本地构建版本（变成 "dev" 或 "dev-1-g<sha>"，被当成 dev 渠道构建）
#   ② 发版说明的对比基线（prev=dev，内容无意义且不可复现）
#   ③ 预览版清理的候选集（不应把 dev 当预览版）
set -euo pipefail
cd "$(dirname "$0")/../.."
repo_root="$PWD"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail=0
assert() { if ! eval "$2"; then echo "FAIL: $1" >&2; fail=1; fi; }

# 造一个「恶意布局」的仓库：dev tag 比任何版本 tag 都近
mk_repo() { # mk_repo <目录> <dev 位置：head|prev>
  local dir="$1" where="$2"
  mkdir -p "$dir" && cd "$dir"
  git init -q . && git config user.email t@t && git config user.name t
  for i in 1 2 3 4 5; do echo "$i" > "f$i"; git add -A; git commit -qm "c$i"; done
  git tag v0.1.0 HEAD~4
  git tag v0.2.0-preview.1 HEAD~3
  if [[ "$where" == head ]]; then git tag dev HEAD; else git tag dev HEAD~1; fi
  git tag v0.2.0-preview.2 HEAD
}

# ① 本地版本计算：dev 落在 HEAD（最坏情形）时也不能变成 dev
mk_repo "$tmp/head" head
v="$(bash "$repo_root/scripts/version.sh")"
assert "dev 在 HEAD 时版本不应含 dev（得到 $v）" "[[ \"\$v\" != *dev* ]]"
assert "版本应退回最近的版本 tag（得到 $v）" "[[ \"\$v\" == v0.* ]]"

# ①' dev 落后一个提交时同理（此时容易变成 dev-1-g<sha>）
mk_repo "$tmp/behind" behind
v="$(bash "$repo_root/scripts/version.sh")"
assert "dev 落后时版本不应含 dev（得到 $v）" "[[ \"\$v\" != *dev* ]]"

# ② 发版对比基线：preview 与 stable 都不能把 dev 当基线
for kind in preview stable; do
  if [[ "$kind" == preview ]]; then tag=v0.2.0-preview.2; else git tag v0.2.0 HEAD; tag=v0.2.0; fi
  prev="$(bash "$repo_root/scripts/release-info.sh" "$tag" | sed -n 's/^prev=//p')"
  assert "release-info($tag) 的 prev 不应是 dev（得到 ${prev:-空}）" "[[ \"\$prev\" != dev ]]"
done

# ③ 预览清理的候选集不含 dev（即便 retention=0、正式版已存在）
GIT_COMMITTER_DATE=2020-01-01T00:00:00Z git commit -q --allow-empty -m old >/dev/null
git tag -f v0.1.0-old-base >/dev/null 2>&1 || true
cd "$tmp/head"
# shellcheck disable=SC1091
source "$repo_root/scripts/prune-preview-releases.sh" --source-only
picked="$(collect_candidates | awk -F'\t' '{print $1}')"
assert "清理候选不应含 dev（得到：${picked:-空}）" "! printf '%s' \"\$picked\" | grep -qx dev"

cd "$repo_root"
if (( fail )); then echo "dev-tag-hygiene: FAILED" >&2; exit 1; fi
echo "dev-tag-hygiene: OK"
