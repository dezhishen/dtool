#!/usr/bin/env bash
# 回归测试：脚本里不允许「$var 紧跟非 ASCII 字符」的写法。
#
# Apple 自带的 bash 3.2（macOS 的 /bin/bash，CI 里 `shell: bash` 用的就是它）会把
# 紧跟的 UTF-8 字节算进变量名：`"$hits、"` 被解析成变量 `hits、`，在 set -u 下直接
# 报 "unbound variable" 退出。v0.4.0-preview.6 的 macOS 构建就是这样挂的
# （gen-notices.sh 的 `${hits:+$hits、}`）。
#
# 规则：变量紧邻非 ASCII 字符时必须写成 ${var} —— 花括号能明确结束变量名。
set -euo pipefail
cd "$(dirname "$0")/../.."

bad="$(LC_ALL=C awk '
  /^[[:space:]]*#/ { next }              # 整行注释不参与
  {
    line = $0
    while (match(line, /\$[A-Za-z_][A-Za-z0-9_]*[^ -~]/)) {
      print FILENAME ":" FNR ": " substr(line, RSTART, RLENGTH)
      line = substr(line, RSTART + RLENGTH)
    }
  }
' scripts/*.sh scripts/tests/*.sh)"

if [[ -n "$bad" ]]; then
  echo 'var-nonascii: 变量紧邻非 ASCII 字符时必须写成 ${var}' >&2
  printf '%s\n' "$bad" >&2
  exit 1
fi

echo "var-nonascii: OK"
