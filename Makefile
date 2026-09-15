# WebMonitoring 构建与运维入口。
#
# Node 工具统一走 corepack，使用 package.json 里 pin 的 pnpm 版本，无需全局安装。
# Windows 下 compose-smoke 走 Windows PowerShell；Linux/CI 用 `make compose-smoke PS=pwsh`。
# golangci-lint 需单独安装（`go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest`）。

PNPM    := corepack pnpm
GO      ?= go
PS      ?= powershell
COMPOSE := docker compose -f deployments/compose/docker-compose.yml

.PHONY: help fmt vet test lint typecheck build compose-up compose-down compose-smoke migrate-up release-check

help: ## 列出可用目标
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-16s %s\n", $$1, $$2}'

fmt: ## 校验 Go 源码格式（gofmt -l）
	@out="$$(gofmt -l $$(git ls-files '*.go'))"; \
	if [ -n "$$out" ]; then printf 'gofmt needed:\n%s\n' "$$out"; exit 1; fi

vet: ## 运行 go vet
	$(GO) vet ./...

test: ## 运行 Go 测试（含配置了集成 DSN 的用例）
	$(GO) test ./...

lint: ## Go（golangci-lint）与前端（pnpm lint）
	golangci-lint run
	$(PNPM) lint

typecheck: ## 前端类型检查
	$(PNPM) typecheck

build: ## 构建 Go 与前端 workspace
	$(GO) build ./...
	$(PNPM) build

compose-up: ## 构建并启动 Compose 栈
	$(COMPOSE) up -d --build

compose-down: ## 停止并移除 Compose 栈
	$(COMPOSE) down

compose-smoke: ## 运行端到端 Compose smoke 测试
	$(PS) -File scripts/compose-smoke.ps1

migrate-up: ## 执行数据库向上迁移（需 Postgres 已启动）
	$(COMPOSE) run --rm migrate up

release-check: ## 校验当前 HEAD 是否带发布 tag
	@git describe --tags --exact-match 2>/dev/null || (echo "no release tag on HEAD" >&2; exit 1)
