BINARY    := dtool
VERSION   ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
VER       := $(VERSION:v%=%)
PLATFORMS := linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64 windows-arm64
# 版本与元数据（commit/分支/构建时间/CI 运行…）由 scripts/ldflags.sh 统一生成
LDFLAGS   := $(shell bash scripts/ldflags.sh $(VER))

.PHONY: build build-all release-build test vet fmt check notices examples examples-data clean help $(addprefix build-,$(PLATFORMS))

help:
	@echo "build               当前平台"
	@echo "build-<os>-<arch>   单个平台，可选: $(PLATFORMS)"
	@echo "build-all           全部平台二进制 -> dist/bin/"
	@echo "release-build       全部平台打包(tar.gz/zip) + checksums.txt -> dist/"
	@echo "check               gofmt + vet + test + 脚本语法"
	@echo "examples            构建并运行全部示例（examples/）"
	@echo "examples-data       重新生成示例数据源"
	@echo "notices             生成 THIRD_PARTY_NOTICES.md（release-build 会自动生成）"

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) .

# 例如 make build-windows-arm64 -> dist/bin/dtool_windows_arm64.exe
$(addprefix build-,$(PLATFORMS)): build-%:
	@mkdir -p dist/bin
	CGO_ENABLED=0 GOOS=$(word 1,$(subst -, ,$*)) GOARCH=$(word 2,$(subst -, ,$*)) \
		go build -trimpath -ldflags="$(LDFLAGS)" \
		-o dist/bin/$(BINARY)_$(subst -,_,$*)$(if $(findstring windows,$*),.exe) .

build-all: $(addprefix build-,$(PLATFORMS))

# 与 GitHub Release 使用同一入口；版本号不带 v 前缀
release-build:
	bash scripts/build-release.sh $(VER)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)
	go vet ./...
	go test ./...
	bash -n scripts/*.sh

# 构建后运行全部示例（输出在 examples/out/）
examples: build
	DTOOL=$(CURDIR)/$(BINARY) bash examples/run-all.sh

# 重新生成示例数据源 examples/data/*.xlsx（固定随机种子，结果可复现）
examples-data:
	go run ./examples/tools/genxlsx

notices:
	bash scripts/gen-notices.sh

clean:
	rm -rf $(BINARY) dist
