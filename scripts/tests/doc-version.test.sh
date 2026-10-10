#!/usr/bin/env bash
# doc-version：安装示例必须「取最新版」，不许写死版本号
#
# 为什么盯这条：示例里的版本号每发一次版就要人肉跟着改一次，漏了就会把过期版本号
# 打进发布包（包内 README/SKILL.md 也正是这么过期的）。而且 `releases/latest` 本来就
# 能拿到最新正式版的 `tag_name`——写上这个，发版流程里就少一个纯手工同步点。
set -euo pipefail
cd "$(dirname "$0")/../.."

fail=0
bad() { echo "FAIL: $1" >&2; fail=1; }

for f in README.md skills/dtool/SKILL.md; do
  if [[ ! -f "$f" ]]; then
    bad "缺文件 $f"
    continue
  fi
  # 可执行的赋值行（`V=v1.2.3` / `$V = "v1.2.3"`）必须已经没有；注释里留「想固定版本就写 V=v1.2.3」允许
  if grep -nE '^[[:space:]]*(V=v[0-9]|\$V[[:space:]]*=[[:space:]]*"?v[0-9])' "$f"; then
    bad "$f 的安装示例写死了版本号：改成从 releases/latest 取 tag_name（注释里可留手动指定版本的办法）"
  fi
  grep -q 'releases/latest' "$f" || bad "$f 的安装示例应从 releases/latest 取最新版本"
done

if (( fail )); then echo "doc-version: FAILED" >&2; exit 1; fi
echo "doc-version: OK（安装示例取最新版，无写死的版本号）"
