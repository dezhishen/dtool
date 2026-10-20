BINARY    := dtool
VERSION   ?= $(shell bash scripts/version.sh) # 排除滚动 dev tag，见脚本注释
VER       := $(VERSION:v%=%)
# 版本与元数据（commit/分支/构建时间/CI 运行…）由 scripts/ldflags.sh 统一生成
LDFLAGS   := $(shell bash scripts/ldflags.sh $(VER))

.PHONY: build release-build test vet fmt check scripts-test bench notices examples examples-data clean help

help:
	@echo "build               当前平台（CGO=1：sqlite + duckdb，需要 gcc）"
	@echo "release-build       当前平台发布包（含 DuckDB）+ checksums.txt -> dist/"
	@echo "check               gofmt + vet + test + 脚本语法 + 脚本单测"
	@echo "scripts-test        scripts/tests/*.test.sh"
	@echo "bench               性能基准（JSON 装入 / Excel 转换）"
	@echo "examples            构建并运行全部示例（examples/）"
	@echo "examples-data       重新生成示例数据源"
	@echo "notices             生成 THIRD_PARTY_NOTICES.md（release-build 会自动生成）"

# 唯一的构建方式：CGO=1。DuckDB 由 internal/query/duckdb.go 的 //go:build cgo
# 自动启用，所以每个二进制都同时支持 sqlite 与 duckdb。
# CGO_ENABLED=0 会在 internal/query/cgo_required.go 处编译失败（明确提示需要 CGO）。
build:
	CGO_ENABLED=1 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) .

# 与 GitHub Release 使用同一入口（版本号不带 v 前缀）：当前平台、含 DuckDB。
# 多平台的发布包由 CI 的原生 runner 矩阵构建（DuckDB 无法从单机交叉编译到所有平台）。
release-build:
	bash scripts/build-release.sh $(VER)

test:
	CGO_ENABLED=1 go test ./...

vet:
	CGO_ENABLED=1 go vet ./...

fmt:
	gofmt -w .

check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)
	CGO_ENABLED=1 go vet ./...
	CGO_ENABLED=1 go test ./...
	bash -n scripts/*.sh
	@$(MAKE) --no-print-directory scripts-test

# scripts/tests/*.test.sh：脚本里的纯逻辑（如预览版清理的选择规则）单测
scripts-test:
	@for f in scripts/tests/*.test.sh; do echo "== $$f"; bash "$$f"; done

# 性能基准：默认 JSON 2 万行、xlsx 1 万行；DTOOL_BENCH_ROWS=N 可放大。
# 内存倍率的回归断言在 go test ./... 里（TestPerf*），这里只跑吞吐。
bench:
	CGO_ENABLED=1 go test -run '^$$' -bench . -benchmem ./internal/query/ ./internal/pipeline/

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
