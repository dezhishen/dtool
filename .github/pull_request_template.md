## 变更内容

<!-- 简述做了什么、为什么 -->

## 类型

- [ ] feature
- [ ] bug fix
- [ ] docs / chore

## 检查清单

- [ ] `make check` 通过（gofmt / vet / test；CGO=1，sqlite + duckdb 一起测）
- [ ] 每个二进制都支持多引擎：CGO=1 是唯一构建方式（DuckDB 由 `//go:build cgo` 自动启用），不引入额外的 `-tags` 或单引擎变体
- [ ] 变更了 CLI 契约（stdout JSON / Action 结构）时已更新 Design.md
