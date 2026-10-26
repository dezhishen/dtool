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

if [[ -z "${1:-}" ]]; then
  echo "usage: build-release.sh <version> [os/arch]" >&2
  echo '提示：版本号为空。若在 Windows 的 pwsh 里调用，"$VAR" 不会展开环境变量' >&2
  echo '      （PowerShell 要写 $env:VAR）；CI 里请给该 step 加 shell: bash。' >&2
  exit 2
fi
version="$1"
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

# 出错时把「哪一步、第几行、什么命令」写成 GitHub annotation（::error::）。
# CI 日志需要仓库权限才能下载，annotation 却可以匿名读，排查时不用再等日志。
step="初始化"
trap 'echo "::error::build-release 在「${step}」失败（第 ${LINENO} 行）：${BASH_COMMAND}" >&2' ERR

ldflags="$(bash scripts/ldflags.sh "$version")" # 各平台共用同一构建时间与元数据
mkdir -p dist
rm -f dist/*.tar.gz dist/*.zip dist/checksums.txt

name="dtool_${version}_${os}_${arch}"
bin="dtool"
[[ "$os" == windows ]] && bin="dtool.exe"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT

# notices 必须与二进制同上下文生成：CGO=1 时会链接 DuckDB。
step="生成 THIRD_PARTY_NOTICES.md"
GOOS="$os" GOARCH="$arch" CGO_ENABLED=1 \
  bash scripts/gen-notices.sh "$stage/THIRD_PARTY_NOTICES.md"

# zip_dir 打包目录内容（与 `cd dir && zip ./*` 等价）。
# Git Bash 不自带 zip：Windows 上退回 PowerShell 7 的 Compress-Archive（.NET 写入
# 标准的 "/" 分隔符；Windows PowerShell 5.1 会写 "\\"，换来跨平台解包问题）。
zip_dir() { # zip_dir <srcdir> <out.zip>
  local src="$1" out="$2"
  if command -v zip >/dev/null 2>&1; then
    (cd "$src" && zip -q "$OLDPWD/${out}" ./*)
    return
  fi
  if command -v pwsh.exe >/dev/null 2>&1 && command -v cygpath >/dev/null 2>&1; then
    local src_win out_win
    src_win="$(cygpath -w "$src")"
    out_win="$(cygpath -w "$PWD/${out}")"
    pwsh.exe -NoProfile -Command \
      "Compress-Archive -Path '${src_win}\\*' -DestinationPath '${out_win}' -Force"
    return
  fi
  echo "没有 zip 命令，也没有可用的 PowerShell（无法生成 ${out}）" >&2
  return 1
}

build() { # build [额外 ldflags]
  # shellcheck disable=SC2086
  CGO_ENABLED=1 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags="${ldflags}${1:-}" -o "${stage}/${bin}" .
}

step="编译 ${target}"
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

# 冒烟：目标平台与当前平台一致时，启动一次产物。Windows 上这会暴露缺失的运行时
# DLL（例如静态链接失败回退成动态链接时），避免把跑不起来的包发出去。
step="冒烟运行产物"
if [[ "$(go env GOOS)/$(go env GOARCH)" == "$target" ]]; then
  "${stage}/${bin}" version >/dev/null
  echo "smoke ok: ${target}"
fi

# 手册的源是标准 Agent Skill 包（skills/<name>/SKILL.md），归档里也按这个名字
# 放：这样解包后既能直接读，也能整份丢进平台的技能目录（<平台目录>/dtool/SKILL.md）。
step="打包归档"
cp README.md LICENSE "${stage}/"
cp skills/dtool/SKILL.md "${stage}/SKILL.md"
if [[ "$os" == windows ]]; then
  zip_dir "$stage" "dist/${name}.zip"
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
