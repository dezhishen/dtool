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
  cp README.md skills.md LICENSE THIRD_PARTY_NOTICES.md "${stage}/"
  if [[ "$os" == windows ]]; then
    (cd "$stage" && zip -q "$OLDPWD/dist/${name}.zip" ./*)
  else
    tar -C "$stage" -czf "dist/${name}.tar.gz" .
  fi
  rm -rf "$stage"
  echo "built ${name}"
done

(cd dist && sha256sum ./*.tar.gz ./*.zip > checksums.txt)
