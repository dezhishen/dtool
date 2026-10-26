#!/usr/bin/env bash
# 生成 Release 说明：正文全部自动推导，不含手写文案。
#
# 自动推导的四块内容：
#   1. 分组条目   —— conventional commits 前缀归类（feat / fix / docs / 其余）
#   2. 破坏性变更 —— `type!:` 或提交正文含 `BREAKING CHANGE`
#   3. 依赖变化   —— 对比基线与本 tag 的 go.mod（新增 / 升级 / 移除）
#   4. 产物       —— dist/ 里的归档 + 上一版 Release 的资产（平台增减、体积变化）
# PR 列表仍走 GitHub generate-notes；仓库不开 PR 时整段自动省略。
#
# 用法: release-notes.sh <tag> <prev-tag|""> [repo] [dist-dir]
set -euo pipefail

tag="${1:?usage: release-notes.sh <tag> <prev-tag> [repo] [dist-dir]}"
prev="${2:-}"
repo="${3:-${GITHUB_REPOSITORY:-dezhishen/dtool}}"
dist="${4:-dist}"

if [[ -n "$prev" ]]; then range="${prev}..${tag}"; else range="$tag"; fi

tmpd="$(mktemp -d)"
trap 'rm -rf "$tmpd"' EXIT

have_gh() { command -v gh >/dev/null 2>&1 && [[ -n "${GH_TOKEN:-${GITHUB_TOKEN:-}}" ]]; }
clink() { printf 'https://github.com/%s/commit/%s' "$repo" "$1"; }
rlink() { printf 'https://github.com/%s/releases/tag/%s' "$repo" "$1"; }

# ---------- 头部：基线 + 规模 ----------
echo "## 变更说明"
echo
if [[ -n "$prev" ]]; then
  echo "对比基线：[\`${prev}\`]($(rlink "$prev")) → \`${tag}\`"
else
  echo "首次发布（仓库内没有更早的版本 tag）。"
fi
ncommits="$(git rev-list --no-merges --count "$range")"
if [[ -n "$prev" ]]; then
  shortstat="$(git diff --shortstat "$prev..$tag" 2>/dev/null || true)"
  files="$(grep -oE '[0-9]+ files? changed' <<<"$shortstat" | grep -oE '[0-9]+' || true)"
  ins="$(grep -oE '[0-9]+ insertions?' <<<"$shortstat" | grep -oE '[0-9]+' || true)"
  del="$(grep -oE '[0-9]+ deletions?' <<<"$shortstat" | grep -oE '[0-9]+' || true)"
  echo
  echo "${ncommits} 个提交，改动 ${files:-0} 个文件（+${ins:-0} / -${del:-0}）。"
fi
echo

# ---------- 按 conventional commits 分组 ----------
breaking_shas="$(git log --no-merges --format=%H --grep='BREAKING CHANGE' "$range" 2>/dev/null || true)"
: >"$tmpd/breaking"
: >"$tmpd/feat"
: >"$tmpd/fix"
: >"$tmpd/docs"
: >"$tmpd/other"

while IFS=$'\x1f' read -r full short subject; do
  [[ -n "$full" ]] || continue
  type="" scope="" bang="" text="$subject"
  if [[ "$subject" =~ ^([A-Za-z]+)(\(([^\)]*)\))?(!)?:[[:space:]]*(.+)$ ]]; then
    type="$(printf '%s' "${BASH_REMATCH[1]}" | tr '[:upper:]' '[:lower:]')"
    scope="${BASH_REMATCH[3]:-}"
    bang="${BASH_REMATCH[4]:-}"
    text="${BASH_REMATCH[5]}"
  fi
  line="- ${text}"
  [[ -n "$scope" ]] && line="${line}（${scope}）"
  line="${line} ([\`${short}\`]($(clink "$full")))"
  case "$type" in
    feat) printf '%s\n' "$line" >>"$tmpd/feat" ;;
    fix) printf '%s\n' "$line" >>"$tmpd/fix" ;;
    docs) printf '%s\n' "$line" >>"$tmpd/docs" ;;
    *) printf '%s\n' "$line" >>"$tmpd/other" ;;
  esac
  if [[ -n "$bang" ]] || { [[ -n "$breaking_shas" ]] && grep -qxF "$full" <<<"$breaking_shas"; }; then
    printf '%s\n' "$line" >>"$tmpd/breaking"
  fi
done < <(git log --no-merges --pretty=format:"%H%x1f%h%x1f%s%x1e" "$range" | tr '\036' '\n')

emit_section() { # $1 = 段标题, $2 = 条目文件（为空则不输出该段）
  [[ -s "$2" ]] || return 0
  echo "### $1"
  echo
  cat "$2"
  echo
}

emit_section "破坏性变更" "$tmpd/breaking"
emit_section "新增功能" "$tmpd/feat"
emit_section "修复" "$tmpd/fix"
emit_section "文档" "$tmpd/docs"
emit_section "其他" "$tmpd/other"

# ---------- 依赖变化（go.mod）----------
mods_of() { # $1 = rev；输出 "模块 版本 direct|indirect"
  git show "$1:go.mod" 2>/dev/null | awk '
    { sub(/\r$/, "") }
    /^require[[:space:]]*\(/ { inb = 1; next }
    inb && /^[[:space:]]*\)/ { inb = 0; next }
    inb && NF >= 2 && $1 !~ /^\/\// && $2 ~ /^v/ {
      print $1, $2, (index($0, "// indirect") ? "indirect" : "direct"); next
    }
    /^require[[:space:]]/ && $3 ~ /^v/ {
      print $2, $3, (index($0, "// indirect") ? "indirect" : "direct")
    }
  ' | LC_ALL=C sort -u
}

if [[ -n "$prev" ]] && mods_of "$prev" >"$tmpd/mod_old" 2>/dev/null \
  && mods_of "$tag" >"$tmpd/mod_new" 2>/dev/null \
  && [[ -s "$tmpd/mod_old" && -s "$tmpd/mod_new" ]]; then
  # 输出列：1=排序权重(1 新增 / 2 升级 / 3 移除) 2=类型 3=模块 4=旧版本 5=新版本 6=direct|indirect
  awk '
    NR == FNR { old[$1] = $2; flag[$1] = $3; order[++n] = $1; next }
    { seen[$1] = 1
      if ($1 in old) {
        if (old[$1] != $2) printf "2\t升级\t%s\t%s\t%s\t%s\n", $1, old[$1], $2, $3
      } else printf "1\t新增\t%s\t-\t%s\t%s\n", $1, $2, $3 }
    END { for (i = 1; i <= n; i++) if (!(order[i] in seen))
            printf "3\t移除\t%s\t%s\t-\t%s\n", order[i], old[order[i]], flag[order[i]] }
  ' "$tmpd/mod_old" "$tmpd/mod_new" >"$tmpd/deps"
  if [[ -s "$tmpd/deps" ]]; then
    echo "### 依赖变化"
    echo
    while IFS=$'\t' read -r _rank kind mod from to flag; do
      extra=""
      [[ "$flag" == indirect ]] && extra="（间接）"
      case "$kind" in
        新增) printf -- '- 新增：`%s` %s%s\n' "$mod" "$to" "$extra" ;;
        升级) printf -- '- 升级：`%s` %s → %s%s\n' "$mod" "$from" "$to" "$extra" ;;
        移除) printf -- '- 移除：`%s` %s%s\n' "$mod" "$from" "$extra" ;;
      esac
    done < <(LC_ALL=C sort -t $'\t' -k1,1n -k6,6 -k3,3 "$tmpd/deps")
    echo
  fi
fi

# ---------- 产物：平台增减与体积 ----------
platform_of() {
  local n="$1"
  n="${n%.tar.gz}"
  n="${n%.zip}"
  printf '%s' "$n" | awk -F_ '{print $(NF-1) "/" $NF}'
}

gh_assets() { # $1 = tag
  gh api "repos/${repo}/releases/tags/${1}" --jq \
    '.assets[] | select(.name | test("\\.(tar\\.gz|zip)$")) | "\(.name)\t\(.size)"' 2>/dev/null \
    | while IFS=$'\t' read -r n s; do printf '%s\t%s\n' "$(platform_of "$n")" "$s"; done
}

collect_current() {
  local f
  shopt -s nullglob
  local files=("$dist"/*.tar.gz "$dist"/*.zip)
  shopt -u nullglob
  if (( ${#files[@]} > 0 )); then
    for f in "${files[@]}"; do
      printf '%s\t%s\n' "$(platform_of "$(basename "$f")")" "$(wc -c <"$f" | tr -d ' ')"
    done
  elif have_gh; then
    gh_assets "$tag"
  fi
}

collect_prev() {
  [[ -n "$prev" ]] || return 0
  have_gh || return 0
  gh_assets "$prev"
}

mb() { awk -v b="$1" 'BEGIN { printf "%.1f MB", b / 1048576 }'; }
delta() { # $1 = 本次字节, $2 = 上一版字节
  awk -v c="$1" -v o="$2" 'BEGIN {
    if (o > 0) { d = c - o; printf "%+.1f MB（%+.0f%%）", d / 1048576, d * 100 / o }
    else printf "—"
  }'
}

collect_current | LC_ALL=C sort >"$tmpd/cur"
collect_prev | LC_ALL=C sort >"$tmpd/pre"
if [[ -s "$tmpd/cur" ]]; then
  echo "### 产物"
  echo
  awk -F'\t' '{print $1}' "$tmpd/cur" | LC_ALL=C sort -u >"$tmpd/cur_p"
  awk -F'\t' '{print $1}' "$tmpd/pre" | LC_ALL=C sort -u >"$tmpd/pre_p"
  # 没有上一版资产时不做"新增/移除平台"的推断：否则全部平台都会被误报成新增。
  dropped=""
  addedp=""
  if [[ -s "$tmpd/pre" ]]; then
    dropped="$(comm -13 "$tmpd/cur_p" "$tmpd/pre_p" 2>/dev/null | tr '\n' ' ' | sed -e 's/ *$//' || true)"
    addedp="$(comm -23 "$tmpd/cur_p" "$tmpd/pre_p" 2>/dev/null | tr '\n' ' ' | sed -e 's/ *$//' || true)"
  fi
  if [[ -s "$tmpd/pre" ]]; then
    echo "| 平台 | 大小 | 较上一版 |"
    echo "| --- | --- | --- |"
    LC_ALL=C join -t $'\t' -a1 -a2 -e- -o '0,1.2,2.2' "$tmpd/cur" "$tmpd/pre" \
      | LC_ALL=C sort \
      | while IFS=$'\t' read -r plat c p; do
          # 本版已不再提供的平台只在下方条目里列出，不进表格（否则会出现 0.0 MB 这种行）
          [[ "$c" == "-" ]] && continue
          if [[ "$p" == "-" ]]; then
            printf '| %s | %s | 新增平台 |\n' "$plat" "$(mb "$c")"
          else
            printf '| %s | %s | %s |\n' "$plat" "$(mb "$c")" "$(delta "$c" "$p")"
          fi
        done
  else
    echo "| 平台 | 大小 |"
    echo "| --- | --- |"
    while IFS=$'\t' read -r plat c; do
      printf '| %s | %s |\n' "$plat" "$(mb "$c")"
    done <"$tmpd/cur"
  fi
  echo
  if [[ -n "${dropped// /}" ]]; then
    echo "- 本版不再提供：${dropped}"
  fi
  if [[ -n "${addedp// /}" ]]; then
    echo "- 本版新增平台：${addedp}"
  fi
  if [[ ! -s "$tmpd/pre" ]]; then
    echo "- 没有可对比的上一版资产（首次发布或取不到上一版 Release）。"
  fi
  echo
fi

# ---------- PR 列表（GitHub generate-notes，仓库不开 PR 时自动省略）----------
prs=""
if have_gh; then
  args=(-f "tag_name=${tag}")
  [[ -n "$prev" ]] && args+=(-f "previous_tag_name=${prev}")
  prs="$(gh api -X POST "repos/${repo}/releases/generate-notes" "${args[@]}" --jq .body 2>/dev/null || true)"
  # 无 PR 时 GitHub 只回 HTML 注释 + Full Changelog 链接，一律剥掉；正文为空就不出这段。
  prs="$(printf '%s\n' "$prs" | awk '
    /^## What.s Changed$/      { next }
    /^<!--.*-->$/              { next }
    /^\*\*Full Changelog\*\*:/ { next }
    /^[[:space:]]*$/           { if (n) blank = 1; next }
    { if (blank) { buf[n++] = ""; blank = 0 } buf[n++] = $0 }
    END { for (i = 0; i < n; i++) print buf[i] }')"
fi
if [[ -n "$prs" ]]; then
  echo "### Pull Requests"
  echo
  printf '%s\n\n' "$prs"
fi

# ---------- 提交明细 ----------
echo "<details><summary>提交明细（${ncommits} 个 commit）</summary>"
echo
git log --no-merges --pretty=format:"- %s ([\`%h\`]($(clink '%H'))) — %an" "$range"
echo
echo
echo "</details>"
