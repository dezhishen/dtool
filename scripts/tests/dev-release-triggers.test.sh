#!/usr/bin/env bash
# dev-release-triggers：dev 滚动发布**只允许**「定时 + 手动」两种触发
#
# 为什么要把这条钉死：dev 是滚动发布，一次触发的代价是六平台构建 + 覆盖上传整套资产。
# 挂上 push 之后，每次提交都会跑一轮，而且会让 workflow 里的 commit 去重必然失效
# （push 时 commit 总是新的）——「避免重复触发」的校验就白写了。要「推完立刻有 dev」，
# 手动触发一次即可（gh workflow run dev-release.yml）。
# 顺便把「on: 段里出现了没预期的键」也挡住：那通常意味着整块 YAML 被改坏了。
set -euo pipefail
cd "$(dirname "$0")/../.."

wf=".github/workflows/dev-release.yml"
fail=0

# 取出 on: 段里的顶层键（顶级 on: 之后、缩进回到 0 之前）
triggers="$(awk '
  /^on:[[:space:]]*$/ { inon = 1; next }
  inon && /^[^[:space:]]/ { inon = 0 }
  inon && /^  [A-Za-z_]+:/ { sub(/^  /, ""); sub(/:.*/, ""); print }
' "$wf" | sort | tr '\n' ' ')"

want="schedule workflow_dispatch "
if [[ "$triggers" != "$want" ]]; then
  echo "FAIL: $wf 的触发方式应为「$want」，实际「$triggers」" >&2
  echo "      只保留定时 + 手动：push 触发会让每次提交都重建并覆盖整套 dev 资产，" >&2
  echo "      还会让 commit 去重失效。想立刻要 dev 请手动触发 workflow。" >&2
  fail=1
fi

# 全文不该再有 push 触发（含注释里残留的旧描述）
if grep -qE '^[[:space:]]*push:' "$wf"; then
  echo "FAIL: $wf 里仍有 push 触发" >&2
  fail=1
fi

# 定时频率与 force 入口是这套设计的另外两半，一起守住
grep -qE '^\s+- cron: "0 \*/4 \* \* \*"' "$wf" || {
  echo "FAIL: $wf 应每 4 小时定时一次（cron \"0 */4 * * *\"）" >&2
  fail=1
}
grep -qE '^\s+force:' "$wf" || {
  echo "FAIL: $wf 的手动触发应保留 force 入口" >&2
  fail=1
}

# 触发事件只可能是这两个，判定脚本要对「commit 未变」跳过（避免定时刷构建号）
grep -q 'dev-release-decision.sh' "$wf" || {
  echo "FAIL: $wf 应在构建前跑 scripts/dev-release-decision.sh 做 commit 去重" >&2
  fail=1
}

if (( fail )); then echo "dev-release-triggers: FAILED" >&2; exit 1; fi
echo "dev-release-triggers: OK（触发方式：定时 + 手动，无 push）"
