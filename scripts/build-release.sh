#!/usr/bin/env bash
# 交叉编译（CGO_ENABLED=0）并打包到 dist/，附 checksums.txt。
# 用法: build-release.sh <version>
set -euo pipefail

version="${1:?usage: build-release.sh <version>}"
cd "$(dirname "$0")/.."
bash scripts/gen-notices.sh THIRD_PARTY_NOTICES.md
ldflags="$(bash scripts/ldflags.sh "$version")" # 各平台共用同一构建时间与元数据
mkdir -p dist
rm -f dist/*.tar.gz dist/*.zip dist/checksums.txt

targets=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64)
for t in "${targets[@]}"; do
  os="${t%/*}" arch="${t#*/}"
  name="dtool_${version}_${os}_${arch}"
  bin="dtool"; [[ "$os" == windows ]] && bin="dtool.exe"
  stage="$(mktemp -d)"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags="${ldflags}" -o "${stage}/${bin}" .
  # 手册的源是标准 Agent Skill 包（skills/<name>/SKILL.md），归档里也按这个名字
  # 放：这样解包后既能直接读，也能整份丢进平台的技能目录（<平台目录>/dtool/SKILL.md）。
  cp README.md LICENSE THIRD_PARTY_NOTICES.md "${stage}/"
  cp skills/dtool/SKILL.md "${stage}/SKILL.md"
  if [[ "$os" == windows ]]; then
    (cd "$stage" && zip -q "$OLDPWD/dist/${name}.zip" ./*)
  else
    tar -C "$stage" -czf "dist/${name}.tar.gz" .
  fi
  rm -rf "$stage"
  echo "built ${name}"
done

(cd dist && sha256sum ./*.tar.gz ./*.zip > checksums.txt)
