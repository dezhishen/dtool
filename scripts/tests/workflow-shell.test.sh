#!/usr/bin/env bash
# workflow-shell：可能落到 Windows 的 job 里，凡是调用 bash 的 step 都必须显式写
# shell: bash（或用一个排除 Windows 的 if）。
#
# 为什么：GitHub Windows runner 的默认 shell 是 pwsh。在那里的 "$VAR" 是 PowerShell
# 变量（未定义为空），环境变量必须写 $env:VAR。dev-release 就曾把空的版本号传给
# build-release.sh，脚本直接报 usage 并失败（dev-39574083191）。
set -euo pipefail
cd "$(dirname "$0")/../.."

fail=0
for f in .github/workflows/*.yml; do
  has_windows=0
  grep -q 'windows-' "$f" && has_windows=1
  out="$(awk -v file="$f" -v has_windows="$has_windows" '
    function flush_step() {
      if (step_line > 0 && uses_bash && !shell_bash && !skip_win && job_maybe_windows) {
        printf "%s:%d: step 调用了 bash，但缺少 \"shell: bash\"（Windows 默认 shell 是 pwsh）\n", file, step_line
        bad++
      }
      step_line = 0; uses_bash = 0; shell_bash = 0; skip_win = 0; in_step = 0
    }
    # 顶层 job（两空格缩进 + 名字 + 冒号）
    /^  [A-Za-z0-9_-]+:[[:space:]]*$/ {
      flush_step()
      job_maybe_windows = 0
      next
    }
    # job 的 runs-on：显式 windows 标签，或 matrix.os 且文件里出现过 windows runner
    /^    runs-on:/ {
      if ($0 ~ /windows/) job_maybe_windows = 1
      else if ($0 ~ /matrix\.os/ && has_windows) job_maybe_windows = 1
      next
    }
    /^      - / { flush_step(); step_line = NR; in_step = 1; next }
    in_step {
      if ($0 ~ /^        shell: bash[[:space:]]*$/) shell_bash = 1
      # 排除 Windows 的 if（Linux/macOS 专用步骤不算风险）
      if ($0 ~ /^        if:/ && $0 ~ /runner\.os[^,]*Linux|runner\.os[^,]*macOS|runner\.os != .Windows/) skip_win = 1
      if ($0 ~ /(^|[^-])bash[[:space:]]/) uses_bash = 1
    }
    END { flush_step(); if (bad > 0) exit 1 }
  ' "$f")" || true
  if [[ -n "$out" ]]; then
    echo "$out" >&2
    fail=1
  fi
done

if (( fail == 0 )); then
  echo "workflow-shell: OK（可能落到 Windows 的 bash step 都显式声明了 shell: bash）"
else
  echo "FAIL: 上面的 step 需要补 shell: bash（或加一个排除 Windows 的 if）" >&2
fi
exit "$fail"
