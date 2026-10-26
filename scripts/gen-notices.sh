#!/usr/bin/env bash
# 生成 THIRD_PARTY_NOTICES.md：汇总**本次构建实际链接进二进制**（CGO=1，含 DuckDB）
# 的第三方模块，并收录各自的许可证全文。内容全部自动生成：
#   - 模块清单      go list -deps
#   - 许可证类型    按每个模块 LICENSE/COPYING/NOTICE 的文本特征识别
#   - 非 MIT 清单   由识别结果推导
# 唯一的人工输入是两组"机器推断不出来的决策"，以数据表形式写在下面的
# DUAL_LICENSE / INLINED_ASSETS 里；生成物本身仍然完全由脚本拼装，不手工编辑。
# 由 build-release.sh 在每次打包前调用，因此发布产物里的清单总与当前依赖一致。
# 某个依赖没有许可证文件时直接失败，避免悄悄发布缺少声明的二进制。
set -euo pipefail
cd "$(dirname "$0")/.."
export CGO_ENABLED="${CGO_ENABLED:-1}"
out="${1:-THIRD_PARTY_NOTICES.md}"

tmpd="$(mktemp -d)"
trap 'rm -rf "$tmpd"' EXIT

# ---------------------------------------------------------------- 数据登记表
# 双许可：模块|并列的许可证|本项目选用（选用后只收录该许可证全文）
DUAL_LICENSE="github.com/golang/freetype|FTL、GPL-2.0|FTL"
# 内嵌资源：模块|源码文件|资源名|许可证|版权归属
INLINED_ASSETS="github.com/wcharczuk/go-chart/v2|roboto/roboto.go|Roboto-Medium.ttf|Apache-2.0|Google"

# ---------------------------------------------------------------- 许可证识别
# 从 stdin 的文本里识别许可证类型，命中多个用"、"连接；识别不出输出"未识别"。
# 用字符串累加而不是数组：macOS 自带的 bash 3.2 在 set -u 下展开空数组会报错。
detect_licenses() {
  local t hits=""
  t="$(tr '[:upper:]' '[:lower:]' | tr -s '[:space:]' ' ')"
  has() { [[ "$t" == *"$1"* ]]; }
  add() { hits="${hits:+$hits、}$1"; }
  has "permission is hereby granted, free of charge" && add "MIT"
  has "permission to use, copy, modify, and/or distribute this software" && add "ISC"
  has "this is free and unencumbered software released into the public domain" && add "Unlicense"
  if has "apache license" && has "version 2.0"; then add "Apache-2.0"; fi
  if has "mozilla public license" && has "2.0"; then add "MPL-2.0"; fi
  if has "redistribution and use in source and binary forms"; then
    if has "neither the name"; then add "BSD-3-Clause"; else add "BSD-2-Clause"; fi
  fi
  if has "gnu general public license"; then
    if has "version 3"; then add "GPL-3.0"; elif has "version 2"; then add "GPL-2.0"; fi
  fi
  has "the freetype project" && add "FTL"
  printf '%s\n' "${hits:-未识别}"
}

# ---------------------------------------------------------------- 收集模块
# Windows 上 go list 的 .Dir 是 `C:\\...\\`，Git Bash 的 find 需要 `C:/...`。
# 排序固定为 C locale：默认 locale（如 zh_CN.UTF-8）下 sort 的整理顺序不同，
# 同一份依赖会生成不同排列的清单，本机产物与 CI 产物就无法逐字节比对。
mods="$(
  go list -deps \
    -f '{{with .Module}}{{if not .Main}}{{.Path}}|{{.Version}}|{{.Dir}}{{end}}{{end}}' . |
    tr '\\' '/' | LC_ALL=C sort -u
)"

: >"$tmpd/mods"    # 模块|版本|目录|识别到的许可证
: >"$tmpd/missing" # 没有许可证文件的模块
while IFS='|' read -r path ver dir; do
  [[ -n "$path" ]] || continue
  files="$(find "$dir" -maxdepth 1 -type f \( -iname 'license*' -o -iname 'licence*' -o -iname 'copying*' -o -iname 'notice*' \) \
    ! -iname 'license-logo*' | LC_ALL=C sort)"
  if [[ -z "$files" ]]; then
    printf '%s\n' "$path" >>"$tmpd/missing"
    lic=""
  else
    lic="$(while IFS= read -r lf; do cat "$lf"; echo; done <<<"$files" | detect_licenses)"
    # 登记了双许可取舍的模块，表里展示生效的那一份（并列情况由下方条目说明）
    dual_chosen="$(awk -F'|' -v m="$path" '$1 == m { print $3; exit }' <<<"$DUAL_LICENSE")"
    [[ -n "$dual_chosen" ]] && lic="$dual_chosen"
  fi
  printf '%s|%s|%s|%s\n' "$path" "$ver" "$dir" "$lic" >>"$tmpd/mods"
done <<<"$mods"

if [[ -s "$tmpd/missing" ]]; then
  echo "gen-notices: 以下模块没有许可证文件，拒绝发布缺少声明的二进制：" >&2
  sed 's/^/  - /' "$tmpd/missing" >&2
  exit 1
fi

unknown="$(awk -F'|' '$4 ~ /未识别/ { printf "%s%s", (n++ ? "、" : ""), $1 }' "$tmpd/mods")"
if [[ -n "$unknown" ]]; then
  echo "gen-notices: 许可证类型未能识别（全文仍会收录，请人工确认）：$unknown" >&2
fi

# ---------------------------------------------------------------- 输出
{
  cat <<'HDR'
# Third-Party Notices

dtool 本身以 MIT 协议发布（见 [LICENSE](LICENSE)）。发布的二进制链接了下列第三方模块，
它们各自的版权声明与许可证全文如下。本文件由 `scripts/gen-notices.sh` 从 `go list -deps`
与各模块实际携带的许可证文件自动生成，请勿手工编辑。

## 许可证概览

| 模块 | 版本 | 生效的许可证 |
| --- | --- | --- |
HDR
  while IFS='|' read -r path ver _dir lic; do
    printf '| `%s` | %s | %s |\n' "$path" "$ver" "${lic:-未识别}"
  done <"$tmpd/mods"
  echo
  dist_line="$(awk -F'|' '
    { n = split($4, a, "、"); for (i = 1; i <= n; i++) c[a[i]]++ }
    END { for (k in c) printf "%s\t%d\n", k, c[k] }' "$tmpd/mods" \
    | LC_ALL=C sort -k2,2nr -k1,1 \
    | awk -F'\t' '{ printf "%s%s %d 个模块", (n++ ? "；" : ""), $1, $2 } END { print "" }')"
  echo "- 许可证类型分布：${dist_line:-（无）}"
  # 宽松许可白名单之外的一律列出：含 copyleft 或需要特别处理的模块会自己浮现出来。
  flagged="$(awk -F'|' '
    { n = split($4, a, "、"); bad = ""
      for (i = 1; i <= n; i++) {
        lic = a[i]
        if (lic == "MIT" || lic == "ISC" || lic == "BSD-2-Clause" || lic == "BSD-3-Clause" ||
            lic == "Apache-2.0" || lic == "Unlicense") continue
        bad = bad (bad == "" ? "" : "、") lic
      }
      if (bad != "") printf "%s`%s` %s（%s）", (m++ ? "、" : ""), $1, $2, bad }
    END { print "" }' "$tmpd/mods")"
  if [[ -n "$flagged" ]]; then
    echo "- 需留意（非宽松许可证，或并列许可证中含 copyleft）：${flagged}"
  else
    echo "- 需留意（非宽松许可证，或并列许可证中含 copyleft）：无。"
  fi
  while IFS='|' read -r m listed chosen; do
    [[ -n "$m" ]] || continue
    echo "- 双许可取舍：\`$m\` 并列 $listed，本项目选用 **$chosen**，下方只收录 $chosen 全文（另一份许可证的全文不收录）。"
  done <<<"$DUAL_LICENSE"
  while IFS='|' read -r m file asset lic owner; do
    [[ -n "$m" ]] || continue
    mdir="$(awk -F'|' -v m="$m" '$1 == m { print $3; exit }' "$tmpd/mods")"
    if [[ -n "$mdir" && -f "$mdir/$file" ]] && grep -q "is the .* of" "$mdir/$file" 2>/dev/null; then
      echo "- 内嵌资源：\`$m\` 的 \`$file\` 内联了 $asset（$lic，版权归 $owner）。"
    else
      echo "gen-notices: 内嵌资源登记未通过校验（$m / $file），请更新脚本里的 INLINED_ASSETS。" >&2
    fi
  done <<<"$INLINED_ASSETS"
  echo

  echo "## 模块清单"
  echo
  while IFS='|' read -r path ver _dir _lic; do
    echo "- \`$path\` $ver"
  done <"$tmpd/mods"
  echo

  while IFS='|' read -r path ver dir _lic; do
    echo "---"
    echo
    echo "## $path $ver"
    echo
    files="$(find "$dir" -maxdepth 1 -type f \( -iname 'license*' -o -iname 'licence*' -o -iname 'copying*' -o -iname 'notice*' \) \
      ! -iname 'license-logo*' | LC_ALL=C sort)"
    # 双许可模块：追加选用的那份许可证全文，并去掉并列的其它许可证文本
    chosen="$(awk -F'|' -v m="$path" '$1 == m { print $3; exit }' <<<"$DUAL_LICENSE")"
    if [[ -n "$chosen" ]]; then
      extra="$dir/licenses/$(printf '%s' "$chosen" | tr '[:upper:]' '[:lower:]').txt"
      [[ -f "$extra" ]] || { echo "gen-notices: $path 声明选用 $chosen，但缺少 $extra" >&2; exit 1; }
      files="$files"$'\n'"$extra"
      files="$(grep -v '^$' <<<"$files" | grep -iv '/license$' || true)$(printf '\n%s' "$dir/LICENSE")"
    fi
    [[ -n "$files" ]] || { echo "gen-notices: no license file found for $path" >&2; exit 1; }
    while IFS= read -r f; do
      [[ -n "$f" ]] || continue
      echo "### ${f#"$dir"/}"
      echo
      echo "~~~~text"
      cat "$f"
      [[ -n "$(tail -c1 "$f")" ]] && echo
      echo "~~~~"
      echo
    done <<<"$files"
  done <"$tmpd/mods"
} >"$out"
echo "wrote $out ($(grep -c '^## [^#]' "$out") sections)"
