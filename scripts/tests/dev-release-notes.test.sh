#!/usr/bin/env bash
# dev-release-notes.sh：正文内容稳定，且能被 $( ) 安全捕获
set -euo pipefail
cd "$(dirname "$0")/../.."

out="$(bash scripts/dev-release-notes.sh dev-38046361651 abc123def456 https://github.com/o/r/actions/runs/1 push)"
fail=0
assert() { if ! eval "$2"; then echo "FAIL: $1" >&2; fail=1; fi; }

assert "标题行" "grep -q '^main 的最新构建（滚动发布' <<<\"\$out\""
assert "版本" "grep -q '^- 版本: dev-38046361651$' <<<\"\$out\""
assert "commit" "grep -q '^- commit: abc123def456$' <<<\"\$out\""
assert "构建链接" "grep -q '^- 构建: https://github.com/o/r/actions/runs/1$' <<<\"\$out\""
assert "触发事件" "grep -q '^- 触发: push$' <<<\"\$out\""
assert "安装提示" "grep -q 'upgrade --channel dev' <<<\"\$out\""
assert "行数固定为 8（多/少行说明转义坏了）" "[[ \$(wc -l <<<\"\$out\") -eq 8 ]]"
assert "没有残留的格式化占位符" "! grep -q '%s' <<<\"\$out\""
# 空参数也不应产出空正文或报错
out2="$(bash scripts/dev-release-notes.sh dev-1 '' '' '')"
assert "空参数也能出正文" "grep -q '^- 版本: dev-1$' <<<\"\$out2\""

if (( fail )); then echo "dev-release-notes: FAILED" >&2; exit 1; fi
echo "dev-release-notes: OK"
