#!/usr/bin/env bash
# 输出本地构建的版本号（Makefile 的 VERSION 默认值）。
#
# 必须**排除滚动的 dev tag**：CI 每次 main 构建都会把 `dev` 移到最新提交，而
# `git describe` 挑的是「最近的 tag」，于是
#   - dev 正落在 HEAD      → 版本变成 "dev"（`dtool version` 显示 channel=local，误导）
#   - dev 落后一个提交      → 版本变成 "dev-1-g<sha>"，会被当成 CI 的 dev 构建
#   - 打版本 tag 时         → 发版说明的对比基线变成 dev（见 release-info.sh）
# 排除后 describe 会退回到最近的版本 tag（形如 v0.2.0-preview.9-1-g1544bc4），
# 没有任何版本 tag 时退回提交号（buildinfo 会判成 local）。
set -euo pipefail
out="$(git describe --tags --always --exclude dev --exclude 'dev-*' 2>/dev/null || true)"
printf '%s\n' "${out:-dev}"
