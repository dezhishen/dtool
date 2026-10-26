#!/usr/bin/env bash
# 生成 Release 说明：正文全部自动推导，不含手写文案。
#
# 自动推导的四块内容：
#   1. 分组条目   —— conventional commits 前缀归类（feat / fix / docs / 其余）
#   2. 破坏性变更 —— `type!:` 或提交正文含 `BREAKING CHANGE`
#   3. 依赖变化   —— 对比基线与本 tag 的 go.mod（新增 / 升级 / 移除）
#   4. 产物       —— dist/ 里的归档 + 上一版 Release 的资产（只列平台与平台增减）
# 不再拼接 GitHub generate-notes 的正文：那会重复列出同一批改动（它按 PR 分类，
# 小节名还会和上面的分组撞车，并带 "New Contributors" 之类的噪音）；PR 编号本身
# 就在提交标题里（如 "(#7)"），提交明细已足够追溯。
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

# ---------- 产物：只列平台 ----------
# 刻意不列体积：预览版之间差几十字节没有信息量，还会稀释"某平台退出支持"这类真信号。
# 归档是否完整由 CI 的启动冒烟与 checksums 兜底，这里只负责"这一版有哪些平台"。
platform_of() {
  local n="$1"
  n="${n%.tar.gz}"
  n="${n%.zip}"
  printf '%s' "$n" | awk -F_ '{print $(NF-1) "/" $NF}'
}

gh_assets() { # $1 = tag；输出平台名
  gh api "repos/${repo}/releases/tags/${1}" --jq '.assets[].name' 2>/dev/null \
    | while IFS= read -r n; do
        case "$n" in
          *.tar.gz | *.zip) platform_of "$n" ;;
        esac
      done
}

collect_current() {
  local f n=0
  shopt -s nullglob
  for f in "$dist"/*.tar.gz "$dist"/*.zip; do
    n=$((n + 1))
    platform_of "$(basename "$f")"
  done
  shopt -u nullglob
  if ((n == 0)) && have_gh; then
    gh_assets "$tag"
  fi
}

collect_prev() {
  [[ -n "$prev" ]] || return 0
  have_gh || return 0
  gh_assets "$prev"
}

join_list() { awk '{ printf "%s%s", (n++ ? "、" : ""), $0 } END { print "" }'; }

collect_current | LC_ALL=C sort -u >"$tmpd/cur"
collect_prev | LC_ALL=C sort -u >"$tmpd/pre"
if [[ -s "$tmpd/cur" ]]; then
  echo "### 产物"
  echo
  printf -- '- 平台：%s\n' "$(join_list <"$tmpd/cur")"
  # 没有上一版资产时不做平台增减推断，否则全部平台都会被误报成新增。
  if [[ -s "$tmpd/pre" ]]; then
    dropped="$(comm -13 "$tmpd/cur" "$tmpd/pre" | join_list)"
    addedp="$(comm -23 "$tmpd/cur" "$tmpd/pre" | join_list)"
    [[ -n "$dropped" ]] && printf -- '- 本版不再提供：%s\n' "$dropped"
    [[ -n "$addedp" ]] && printf -- '- 本版新增平台：%s\n' "$addedp"
  else
    echo "- 没有可对比的上一版资产（首次发布或取不到上一版 Release），未做平台增减推断。"
  fi
  echo
fi

# ---------- 提交明细 ----------
echo "<details><summary>提交明细（${ncommits} 个 commit）</summary>"
echo
git log --no-merges --pretty=format:"- %s ([\`%h\`]($(clink '%H'))) — %an" "$range"
echo
echo
echo "</details>"
