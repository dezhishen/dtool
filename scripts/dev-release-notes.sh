#!/usr/bin/env bash
# 输出滚动 dev 发布的说明正文（stdout）。
#
# 抽成脚本的原因：这段以前写在 workflow 的 `$(printf '...' \` 续行里，反斜杠续行在 YAML
# 里极易被写坏（落成两个反斜杠时，第二行参数会被 shell 当成命令执行——CI 上实测炸过），
# 而且文本内容没法单测。放这里既有单测，workflow 里也只剩一行调用。
#
# 用法: dev-release-notes.sh <dev 版本> <commit> <本次运行链接> <触发事件>
set -euo pipefail

version="${1:?usage: dev-release-notes.sh <version> <commit> <run-url> <event>}"
commit="${2:-unknown}"
run_url="${3:-}"
event="${4:-unknown}"

cat <<EOF
main 的最新构建（滚动发布：资产每次构建覆盖，只保留最新一次）

- 版本: ${version}
- commit: ${commit}
- 构建: ${run_url}
- 触发: ${event}

安装：\`dtool upgrade --channel dev\`；查看指定构建请用该次 workflow 的 artifact。
EOF
