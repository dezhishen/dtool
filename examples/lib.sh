#!/usr/bin/env bash
# 示例公共函数：source 后使用 demo_init / dt。
set -euo pipefail

EX_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DATA="$EX_DIR/data"

# 优先用环境变量 DTOOL，其次 PATH 中的 dtool，最后仓库根目录 make build 的产物
if [[ -z "${DTOOL:-}" ]]; then
  if command -v dtool >/dev/null 2>&1; then DTOOL=dtool
  elif [[ -x "$EX_DIR/../dtool" ]]; then DTOOL="$EX_DIR/../dtool"
  else echo "找不到 dtool：请先在仓库根目录执行 make build，或设置 DTOOL=/path/to/dtool" >&2; exit 1
  fi
fi

# 图表中文字体：默认自动发现系统字体；也可 FONT=/path/to/font.ttf 指定
FONT_ARGS=()
[[ -n "${FONT:-}" ]] && FONT_ARGS=(--font "$FONT")

# demo_init <名称>：进入全新的输出目录（工作区 .dtool 与图表/报表都在其中）
demo_init() {
  OUT="$EX_DIR/out/$1"
  rm -rf "$OUT" && mkdir -p "$OUT"
  cd "$OUT"
  echo "输出目录：$OUT"
}

# dt <dtool 参数...>：打印并执行命令
dt() {
  printf '\n\033[1;36m$ dtool'
  printf ' %q' "$@"
  printf '\033[0m\n'
  "$DTOOL" "$@"
}

# viz 与 dt 相同，但自动附带字体参数
viz() { dt visualize "$@" ${FONT_ARGS[@]+"${FONT_ARGS[@]}"}; }
