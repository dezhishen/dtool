#!/usr/bin/env bash
# 依次运行全部示例：bash examples/run-all.sh（输出在 examples/out/，已被 .gitignore 忽略）
set -euo pipefail
cd "$(dirname "$0")"
for d in 0*/; do
  echo; echo "================ ${d%/} ================"
  bash "$d/run.sh"
done
echo; echo "全部示例运行完成。"
