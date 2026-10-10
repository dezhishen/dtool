#!/usr/bin/env bash
# 清理「正式版已经发布」的预览版 Release（release + tag）。
#
# 规则：
#   - 只处理 vX.Y.0-preview.N；只有对应的正式版 vX.Y.0 **已经存在**，才算「被正式版取代」；
#   - 保留期从**正式版发布日**起算（默认 14 天）：到期才删，没到期不动；
#   - 删除 Release 时用 --cleanup-tag 一并删掉 tag；若该 tag 根本没有 Release，则只删 tag 引用；
#   - 任何时候都不会碰正式版/补丁版（vX.Y.0、vX.Y.Z）。
#
# 用法：
#   scripts/prune-preview-releases.sh [--dry-run] [--retention-days N] [--repo owner/name]
# 环境变量：RETENTION_DAYS / DRY_RUN / GITHUB_REPOSITORY；删除需要 GH_TOKEN（gh CLI）。
set -euo pipefail

SOURCE_ONLY=0

RETAIN_DAYS="${RETENTION_DAYS:-14}"
DRY="${DRY_RUN:-0}"
REPO="${GITHUB_REPOSITORY:-dezhishen/dtool}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --source-only) SOURCE_ONLY=1; shift ;;
    --dry-run) DRY=1; shift ;;
    --retention-days) RETAIN_DAYS="${2:?--retention-days 需要天数}"; shift 2 ;;
    --repo) REPO="${2:?--repo 需要 owner/name}"; shift 2 ;;
    -h|--help) sed -n '2,14p' "$0"; exit 0 ;;
    *) echo "未知参数：$1" >&2; exit 2 ;;
  esac
done

# select_prune：从 stdin 读 "preview_tag<TAB>stable_tag<TAB>stable_epoch"，
# stdout 输出可以删除的 preview_tag。保留期按正式版发布日起算。
# 单独抽出来是为了能在没有网络、没有 gh 的情况下单测。
select_prune() {
  local days="$1" now="$2"
  awk -F'\t' -v days="$days" -v now="$now" \
    '$2 != "" && $3 != "" && now - $3 >= days * 86400 { print $1 }'
}

# tag_epoch：取 tag 的创建时间（annotated tag 用 taggerdate，轻量 tag 退回提交时间）。
tag_epoch() {
  local t="$1" e=""
  e="$(git for-each-ref --format='%(taggerdate:unix)' "refs/tags/$t" 2>/dev/null || true)"
  if [[ -z "$e" || "$e" == 0 ]]; then
    e="$(git log -1 --format=%ct "$t" 2>/dev/null || true)"
  fi
  printf '%s' "$e"
}

# collect_candidates：列出所有「正式版已存在」的预览版，输出给 select_prune。
collect_candidates() {
  local t stable epoch
  while read -r t; do
    [[ -n "$t" ]] || continue
    if [[ ! "$t" =~ ^v([0-9]+)\.([0-9]+)\.0-preview\.[0-9]+$ ]]; then continue; fi
    stable="v${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.0"
    git rev-parse -q --verify "refs/tags/$stable" >/dev/null 2>&1 || continue
    epoch="$(tag_epoch "$stable")"
    printf '%s\t%s\t%s\n' "$t" "$stable" "$epoch"
  done < <(git tag -l 'v*-preview.*' | sort -V)
}

# 测试 source 本文件时只取函数，不执行主流程。
if [[ "$SOURCE_ONLY" == 1 ]]; then return 0 2>/dev/null || exit 0; fi

now="$(date +%s)"
targets="$(collect_candidates | select_prune "$RETAIN_DAYS" "$now")"

echo "保留策略：正式版发布满 ${RETAIN_DAYS} 天后清理其预览版；基准时间 $(date -u +%FT%TZ)"
if [[ -z "$targets" ]]; then
  echo "无需清理。"
  exit 0
fi

count=0
while read -r tag; do
  [[ -n "$tag" ]] || continue
  if [[ "$DRY" == 1 ]]; then
    echo "[dry-run] 将删除 release + tag：$tag"
    count=$((count + 1))
    continue
  fi
  if ! command -v gh >/dev/null 2>&1; then
    echo "找不到 gh CLI：删除需要 gh 与 GH_TOKEN" >&2
    exit 1
  fi
  # 有 Release 就连 tag 一起删；没有 Release 就只删 tag 引用。
  if gh release delete "$tag" --repo "$REPO" --cleanup-tag --yes >/dev/null 2>&1; then
    echo "已删除 release + tag：$tag"
  elif git push "$(git remote | head -1)" ":refs/tags/$tag" >/dev/null 2>&1; then
    echo "已删除 tag（该 tag 没有 Release）：$tag"
  else
    echo "删除失败：$tag" >&2
    exit 1
  fi
  count=$((count + 1))
done <<< "$targets"

echo "完成：处理 ${count} 个预览版。"
