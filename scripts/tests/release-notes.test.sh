#!/usr/bin/env bash
# release-notes.sh 的单测：说明正文必须全部由数据推导（提交分类、依赖增删、产物对比、
# 提交明细），并且在没有 PR / 没有 gh 时不留空段。用假 gh 喂固定响应，不碰网络。
set -euo pipefail
cd "$(dirname "$0")/../.."
script="$PWD/scripts/release-notes.sh"

fail() { echo "release-notes: $*" >&2; exit 1; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

repo="$tmp/repo"
mkdir -p "$repo"
cd "$repo"
git init -q .
git config user.email t@example.com
git config user.name t

cat >go.mod <<'EOF'
module example.com/x

go 1.21

require (
	example.com/old v1.0.0
	example.com/gone v0.9.0
)
EOF
echo x >f
git add go.mod f
git commit -qm "chore: init"
git tag v0.1.0

cat >go.mod <<'EOF'
module example.com/x

go 1.21

require example.com/old v1.1.0

require (
	example.com/new v0.2.0 // indirect
)
EOF
git commit -qam "feat: 新增 X"
git commit -qm "fix(core): 修复 Y" --allow-empty
git commit -qm "docs: 文档 Z" --allow-empty
git commit -qm "chore: 杂务" --allow-empty
git commit -qm "feat!: 破坏 A" --allow-empty
git tag v0.2.0

# 产物：本版两个平台；上一版多一个 windows/arm64（模拟平台被裁掉）
mkdir -p "$tmp/dist"
printf 'x' >"$tmp/dist/dtool_0.2.0_linux_amd64.tar.gz"
head -c 2097152 /dev/zero >"$tmp/dist/dtool_0.2.0_windows_amd64.zip"

mkdir -p "$tmp/bin"
cat >"$tmp/bin/gh" <<'EOF'
#!/usr/bin/env bash
case "$*" in
  *generate-notes*) cat "$FAKE_BODY" ;;
  *"/releases/tags/"*) cat "$FAKE_ASSETS" 2>/dev/null || true ;;
  *) exit 1 ;;
esac
EOF
chmod +x "$tmp/bin/gh"

# GitHub 在"无 PR"时返回的形状：只有 HTML 注释与 Full Changelog 链接
printf '%s\n' \
  '<!-- Release notes generated using configuration in .github/release.yml at v0.2.0 -->' \
  '' '' \
  '**Full Changelog**: https://example.invalid/compare/v0.1.0...v0.2.0' >"$tmp/empty.md"
printf 'dtool_0.1.0_linux_amd64.tar.gz\t1048576\n' >"$tmp/assets.txt"
printf 'dtool_0.1.0_windows_arm64.zip\t1024\n' >>"$tmp/assets.txt"

out="$(PATH="$tmp/bin:$PATH" GH_TOKEN=x FAKE_BODY="$tmp/empty.md" FAKE_ASSETS="$tmp/assets.txt" \
  bash "$script" v0.2.0 v0.1.0 example.com/x "$tmp/dist")"

# 1) 提交按类型自动分组
grep -q '### 新增功能' <<<"$out" || fail "缺少 新增功能 段"
grep -q -- '- 新增 X ' <<<"$out" || fail "feat 提交未归入新增功能"
grep -q '### 修复' <<<"$out" || fail "缺少 修复 段"
grep -q -- '- 修复 Y（core）' <<<"$out" || fail "fix 提交未带上 scope"
grep -q '### 文档' <<<"$out" || fail "缺少 文档 段"
grep -q '### 其他' <<<"$out" || fail "缺少 其他 段"

# 2) 破坏性变更：type! 自动识别
grep -q '### 破坏性变更' <<<"$out" || fail "缺少 破坏性变更 段"
grep -q -- '- 破坏 A ' <<<"$out" || fail "feat! 未识别为破坏性变更"

# 3) 依赖变化：新增（含间接标记）/ 升级 / 移除
grep -q '### 依赖变化' <<<"$out" || fail "缺少 依赖变化 段"
grep -q -- '- 新增：`example.com/new` v0.2.0（间接）' <<<"$out" || fail "未识别新增依赖"
grep -q -- '- 升级：`example.com/old` v1.0.0 → v1.1.0' <<<"$out" || fail "未识别升级依赖"
grep -q -- '- 移除：`example.com/gone` v0.9.0' <<<"$out" || fail "未识别移除依赖"

# 4) 产物：平台增减与体积都从实物推导
grep -q '### 产物' <<<"$out" || fail "缺少 产物 段"
grep -q '| linux/amd64 |' <<<"$out" || fail "缺少 linux/amd64 行"
grep -q '本版不再提供：windows/arm64' <<<"$out" || fail "未识别被裁掉的平台"
grep -qE '^\| windows/arm64' <<<"$out" && fail "已裁掉的平台不应出现在产物表格里"
grep -q -- '-1.0 MB（-100%）' <<<"$out" || fail "未计算体积变化"

# 5) 无 PR 时不留空段，也不留占位文本
grep -q '### Pull Requests' <<<"$out" && fail "无 PR 时不应输出 Pull Requests 段"
grep -q 'Release notes generated' <<<"$out" && fail "不应保留 GitHub 的 HTML 注释"
grep -q '_无可用的 PR 信息_' <<<"$out" && fail "不应输出占位文本"

# 6) 提交明细可折叠且计数正确（5 个 commit）
grep -q '<details><summary>提交明细（5 个 commit）</summary>' <<<"$out" || fail "提交明细计数不符"
grep -q '</details>' <<<"$out" || fail "提交明细未闭合"

# 7) 没有 gh / token 时仍能生成，且不做平台增减推断
out2="$(env -u GH_TOKEN -u GITHUB_TOKEN PATH="$tmp/bin:$PATH" FAKE_BODY=/dev/null FAKE_ASSETS=/dev/null \
  bash "$script" v0.2.0 v0.1.0 example.com/x "$tmp/dist")"
grep -q '### Commits\|提交明细' <<<"$out2" || fail "缺少提交明细"
grep -q '本版不再提供' <<<"$out2" && fail "拿不到上一版资产时不应推断平台增减"
grep -q '没有可对比的上一版资产' <<<"$out2" || fail "缺少无上一版资产的说明"

# 8) 没有产物时整段省略，不输出空表
out3="$(PATH="$tmp/bin:$PATH" GH_TOKEN=x FAKE_BODY="$tmp/empty.md" FAKE_ASSETS=/dev/null \
  bash "$script" v0.2.0 v0.1.0 example.com/x "$tmp/no-such-dist")"
grep -q '### 产物' <<<"$out3" && fail "没有产物时不应输出 产物 段"

echo "release-notes: OK"
