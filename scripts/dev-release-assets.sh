#!/usr/bin/env bash
# 把 dist/ 里的 dev 构建改成「滚动 dev 发布」的资产布局：
#   dtool_dev-<run id>_<os>_<arch>.<ext>  →  dtool_dev_<os>_<arch>.<ext>
# 并重算 checksums.txt、写出 dev-build.txt（构建号的权威来源）。
#
# 为什么资产名要固定：`dtool upgrade --channel dev` 的目标是「main 的最新构建」，
# 每次都覆盖同名资产即可，不必按次建 tag/Release（也就没有清理负担）。
# 为什么改名后必须重算 checksums：升级端是按**资产名**去 checksums.txt 里找哈希的。
#
# 用法: dev-release-assets.sh <dist 目录> <dev 版本，如 dev-38043572835>
# 环境: BUILD_ID / COMMIT / BUILT_AT / BUILD_URL（缺省时按 GITHUB_* 推断）
set -euo pipefail

dist="${1:?usage: dev-release-assets.sh <dist> <dev-version>}"
version="${2:?usage: dev-release-assets.sh <dist> <dev-version>}"
cd "$dist"

shopt -s nullglob
archives=(dtool_"${version}"_*.tar.gz dtool_"${version}"_*.zip)
if (( ${#archives[@]} == 0 )); then
  echo "no archives for ${version} in $(pwd)" >&2
  exit 1
fi
if [[ -e "${version}" || -n "$(echo "${version}"_* 2>/dev/null)" ]]; then :; fi

for f in "${archives[@]}"; do
  mv "$f" "${f/dtool_${version}_/dtool_dev_}"
done

sha256sum ./*.tar.gz ./*.zip > checksums.txt

build_id="${BUILD_ID:-${GITHUB_RUN_ID:-}}"
commit="${COMMIT:-${GITHUB_SHA:-}}"
built_at="${BUILT_AT:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
build_url="${BUILD_URL:-}"
if [[ -z "$build_url" && -n "${GITHUB_SERVER_URL:-}" && -n "${GITHUB_REPOSITORY:-}" && -n "${GITHUB_RUN_ID:-}" ]]; then
  build_url="${GITHUB_SERVER_URL}/${GITHUB_REPOSITORY}/actions/runs/${GITHUB_RUN_ID}"
fi
# 第一行必须是构建号：升级端只读第一行非空行
printf 'dev-%s\n%s\n%s\n%s\n' "${build_id:-unknown}" "${commit:-unknown}" "$built_at" "$build_url" > dev-build.txt

echo "dev 资产就绪："
ls -1 ./*.tar.gz ./*.zip checksums.txt dev-build.txt
