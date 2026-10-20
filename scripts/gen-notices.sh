#!/usr/bin/env bash
# 生成 THIRD_PARTY_NOTICES.md：汇总**本次构建实际链接进二进制**的第三方模块的许可证全文。
# 由 build-release.sh 在每次打包前调用，因此发布产物里的清单总与当前依赖一致，无需手工维护或提交。
# CGO=1 的构建会自动链接 DuckDB（见 internal/query/duckdb.go 的 //go:build cgo），
# 必须用同一构建上下文列依赖；GOOS/GOARCH 由调用方通过环境变量指定（CI 原生矩阵）。
# 某个依赖没有许可证文件时直接失败，避免悄悄发布缺少声明的二进制。
set -euo pipefail
cd "$(dirname "$0")/.."
export CGO_ENABLED="${CGO_ENABLED:-1}"
out="${1:-THIRD_PARTY_NOTICES.md}"

# Windows 上 go list 的 .Dir 是 `C:\\...\\`，Git Bash 的 find 需要 `C:/...`，
# 统一成正斜杠（Linux 上是 no-op）。
mods="$(
  go list -deps \
    -f '{{with .Module}}{{if not .Main}}{{.Path}}|{{.Version}}|{{.Dir}}{{end}}{{end}}' . |
    tr '\\' '/' | sort -u
)"

{
  cat <<'HDR'
# Third-Party Notices

dtool 本身以 MIT 协议发布（见 [LICENSE](LICENSE)）。发布的二进制静态链接了下列第三方模块，
它们各自的版权声明与许可证全文如下。本文件由 `scripts/gen-notices.sh` 生成，请勿手工编辑。

## 特别说明

- **github.com/golang/freetype** 采用 FreeType 与 GPLv2 双协议，**dtool 选择 FreeType License (FTL)**（全文见下）。
  Portions of this software are copyright © The FreeType Project (www.freetype.org). All rights reserved.
- **DuckDB**：`github.com/duckdb/duckdb-go/v2`、`github.com/duckdb/duckdb-go-bindings`
  及其各平台预编译库均以 MIT 发布，版权归 Stichting DuckDB Foundation 等；许可证全文见下方条目。
- **Roboto 字体**：图表库 `github.com/wcharczuk/go-chart/v2` 的 `roboto` 包内嵌了 Google 的 Roboto 字体数据，
  该字体以 Apache License 2.0 发布（许可证全文见下方 Apache-2.0 条目，如 `github.com/spf13/cobra`）。
- 运行时按需加载的系统字体（如 `--font` 指定或自动发现的 CJK 字体）不随 dtool 分发，其许可证由字体自身决定。

HDR
  echo "## 模块清单"
  echo
  while IFS='|' read -r path ver _; do echo "- \`$path\` $ver"; done <<<"$mods"
  echo
  while IFS='|' read -r path ver dir; do
    echo "---"
    echo
    echo "## $path $ver"
    echo
    files="$(find "$dir" -maxdepth 1 -type f \( -iname 'license*' -o -iname 'licence*' -o -iname 'copying*' -o -iname 'notice*' \) \
      ! -iname 'license-logo*' | sort)"
    if [[ "$path" == "github.com/golang/freetype" ]]; then
      files="$files"$'\n'"$dir/licenses/ftl.txt"   # 选用的 FTL 全文；不含 GPL 文本
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
  done <<<"$mods"
} >"$out"
echo "wrote $out ($(grep -c '^## ' "$out") sections)"
