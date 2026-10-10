#!/usr/bin/env bash
# dev-release-assets.sh：资产改名、校验和重算、构建号文件（升级端依赖这份约定）
set -euo pipefail
cd "$(dirname "$0")/../.."

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
dist="$tmp/dist"
mkdir -p "$dist"
version="dev-38043572835"
for t in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64; do
  printf '%s' "$t" > "$dist/dtool_${version}_${t}.tar.gz"
done
for t in windows_amd64 windows_arm64; do
  printf '%s' "$t" > "$dist/dtool_${version}_${t}.zip"
done

BUILD_ID=38043572835 COMMIT=deadbeef BUILT_AT=2026-10-10T00:00:00Z BUILD_URL=https://example/run/1 \
  bash scripts/dev-release-assets.sh "$dist" "$version" >/dev/null

fail=0
assert() { # assert <描述> <条件>
  if ! eval "$2"; then echo "FAIL: $1" >&2; fail=1; fi
}

# 1) 六个平台资产都改成了固定名，且不再带 run id
for t in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64; do
  assert "缺资产 dtool_dev_${t}.tar.gz" "[[ -f '$dist/dtool_dev_${t}.tar.gz' ]]"
done
for t in windows_amd64 windows_arm64; do
  assert "缺资产 dtool_dev_${t}.zip" "[[ -f '$dist/dtool_dev_${t}.zip' ]]"
done
assert "不应残留带 run id 的资产" "! compgen -G '$dist/dtool_${version}_*' >/dev/null"

# 2) checksums.txt 必须覆盖改名后的文件（升级端按资产名找哈希）
assert "checksums 数量应为 6" "[[ \$(grep -c '^\S\{64\}  \./' '$dist/checksums.txt') -eq 6 ]]"
for t in linux_amd64 windows_arm64; do
  ext=tar.gz; [[ "$t" == windows_* ]] && ext=zip
  assert "checksums 缺少 dtool_dev_${t}.${ext}" "grep -q '\./dtool_dev_${t}\.${ext}\$' '$dist/checksums.txt'"
done

# 3) dev-build.txt 第一行是构建号（升级端只读第一行非空行）
assert "dev-build.txt 首行应为构建号" \
  '[[ "$(head -1 "$dist/dev-build.txt")" == "dev-38043572835" ]]'
assert "dev-build.txt 应含 commit" "grep -q '^deadbeef$' '$dist/dev-build.txt'"

# 4) 重跑必须失败：脚本是「把带 run id 的资产改成固定名」，输入已被改名后应明确报错，
#    而不是产出一份缺文件的发布（CI 每次都在全新 dist 上跑一次；覆盖语义由 gh --clobber 负责）
if bash scripts/dev-release-assets.sh "$dist" "$version" >/dev/null 2>&1; then
  echo "FAIL: 输入已被改名时应报错" >&2; fail=1
fi

# 5) 没有对应版本时应当报错退出（避免把空发布推上去）
if bash scripts/dev-release-assets.sh "$dist" "dev-1" >/dev/null 2>&1; then
  echo "FAIL: 版本不存在时应报错" >&2; fail=1
fi

if (( fail )); then echo "dev-release-assets: FAILED" >&2; exit 1; fi
echo "dev-release-assets: OK"
