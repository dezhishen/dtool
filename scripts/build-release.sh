#!/usr/bin/env bash
# 构建**当前目标平台**的发布包（含 DuckDB 后端），附 checksums.txt。
# DuckDB 由 CGO 自动启用（internal/query/duckdb.go 的 //go:build cgo），不需要 -tags。
#
# 用法: build-release.sh <version> [os/arch]
#   os/arch 省略时用 go env 的当前平台。CI 用原生 runner 矩阵，每台机器只出一份。
#
# 为什么不再一台机器交叉编译六个平台：DuckDB 通过 CGO 链接平台预编译静态库
# （linux/darwin amd64+arm64、windows amd64），交叉编译需要对应 C 工具链。
# 注意：没有 windows/arm64 的 DuckDB 预编译库，该平台不再随发布提供。
#
# Linux/Windows 优先全静态链接，发布物仍然「下载即用」；静态链接失败时回退为
# 动态链接并明确告警。macOS 没有静态 libSystem，保持系统动态库。
set -euo pipefail

version="${1:?usage: build-release.sh <version> [os/arch]}"
target="${2:-$(go env GOOS)/$(go env GOARCH)}"
os="${target%/*}"
arch="${target#*/}"
case "$os" in
  linux | darwin | windows) ;;
  *)
    echo "不支持的平台：$target（可选 linux/darwin/windows）" >&2
    exit 1
    ;;
esac

cd "$(dirname "$0")/.."
ldflags="$(bash scripts/ldflags.sh "$version")" # 各平台共用同一构建时间与元数据
mkdir -p dist
rm -f dist/*.tar.gz dist/*.zip dist/checksums.txt

name="dtool_${version}_${os}_${arch}"
bin="dtool"
[[ "$os" == windows ]] && bin="dtool.exe"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT

# notices 必须与二进制同上下文生成：CGO=1 时会链接 DuckDB。
GOOS="$os" GOARCH="$arch" CGO_ENABLED=1 \
  bash scripts/gen-notices.sh "$stage/THIRD_PARTY_NOTICES.md"

build() { # build [额外 ldflags]
  # shellcheck disable=SC2086
  CGO_ENABLED=1 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags="${ldflags}${1:-}" -o "${stage}/${bin}" .
}

static=1
[[ "$os" == darwin ]] && static=0
if ((static)); then
  if ! build ' -linkmode external -extldflags=-static'; then
    echo "警告：${target} 静态链接失败，回退为动态链接（目标机需要对应的运行时库，如 glibc/libstdc++）" >&2
    build
  fi
else
  build
fi

# 手册的源是标准 Agent Skill 包（skills/<name>/SKILL.md），归档里也按这个名字
# 放：这样解包后既能直接读，也能整份丢进平台的技能目录（<平台目录>/dtool/SKILL.md）。
cp README.md LICENSE "${stage}/"
cp skills/dtool/SKILL.md "${stage}/SKILL.md"
if [[ "$os" == windows ]]; then
  (cd "$stage" && zip -q "$OLDPWD/dist/${name}.zip" ./*)
else
  tar -C "$stage" -czf "dist/${name}.tar.gz" .
fi

shopt -s nullglob
archives=("dist"/*.tar.gz "dist"/*.zip)
((${#archives[@]})) || {
  echo "没有产物可打包：${name}" >&2
  exit 1
}
(cd dist && sha256sum "${archives[@]##*/}" > checksums.txt)
echo "built ${name}"
