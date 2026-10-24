# FreeRouter — 构建与开发入口
#
# 为什么缓存要显式指到工作区内：本机 ~/go 与 ~/.cache/go-build 在某些沙箱环境下不可写
# （go: mkdir /home/<user>/.local/share/go/pkg/mod/...: read-only file system）。
# 把 GOMODCACHE / GOCACHE 固定到仓库内的 .cache/ 后，构建在任何环境下都可复现。
# .cache/ 已在 .gitignore 中，不会入库。

SHELL := /bin/bash

BINARY     := bin/freerouter
GO         ?= go
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PKG        := github.com/LittleFishStars/FreeRouter

export GOMODCACHE ?= $(CURDIR)/.cache/gomod
export GOCACHE    ?= $(CURDIR)/.cache/gobuild
export GOPATH     ?= $(CURDIR)/.cache/gopath
export GOFLAGS    ?= -mod=mod

LDFLAGS := -X $(PKG)/internal/version.Version=$(VERSION) \
           -X $(PKG)/internal/version.Commit=$(COMMIT) \
           -X $(PKG)/internal/version.BuildTime=$(BUILD_TIME)

.PHONY: all build release test test-race vet fmt tidy run print-config check-config contract clean web help docs-baseline docs-check docs-brief

all: build

## build: 编译主二进制（不含前端，控制台是占位页；仅作快速编译检查）
build:
	@mkdir -p bin
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/freerouter
	@echo "已构建 $(BINARY)：$(VERSION) ($(COMMIT))——不含前端，完整版用 make release"

## release: 构建内嵌前端的完整单二进制（日常运行用这个；先跑 make web）
release:
	@if [ ! -f internal/webui/dist/index.html ]; then \
		echo "internal/webui/dist 不存在：先跑 make web 构建前端" >&2; exit 1; \
	fi
	@mkdir -p bin
	$(GO) build -trimpath -tags webui_dist -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/freerouter
	@echo "已构建 $(BINARY)：$(VERSION) ($(COMMIT))，内嵌前端"

## test: 跑全部单元测试
test:
	$(GO) test ./...

## test-race: 带竞态检测跑测试（并发相关的改动必须过这一关）
test-race:
	$(GO) test -race ./...

## contract: 只跑契约测试——跨进程/跨里程碑的接口一旦漂移必须在这里失败
contract:
	$(GO) test -run 'Contract' -v ./...

## vet: 静态检查
vet:
	$(GO) vet ./...

## fmt: 格式化
fmt:
	$(GO) fmt ./...

## tidy: 整理依赖
tidy:
	$(GO) mod tidy

## run: 以 testdata/config.yaml 启动（前台运行）
run: build
	./$(BINARY) --config testdata/config.yaml

## print-config: 打印生效配置（含 FR_ 环境变量覆盖）后退出
print-config: build
	./$(BINARY) --config testdata/config.yaml --print-config

## check-config: 只校验配置
check-config: build
	./$(BINARY) --config testdata/config.yaml --check-config

## web: 构建前端并嵌入（M9；在此之前是可选的）
web:
	@if [ -d web ] && [ -f web/package.json ]; then \
		cd web && (pnpm install --frozen-lockfile && pnpm build); \
	else \
		echo "web/ 尚未就绪，跳过"; \
	fi

## docs-baseline: 记录设计文档基线（用户改动文档后重记）
docs-baseline:
	python3 scripts/docs_fingerprint.py baseline

## docs-check: 检查设计文档是否变更；有变更时退出非零（合并前必跑）
docs-check:
	@python3 scripts/docs_fingerprint.py check

## docs-brief: 输出可直接贴给队友的文档变更提醒
docs-brief:
	@python3 scripts/docs_fingerprint.py check --brief


clean:
	rm -rf bin dist
	rm -rf .cache

## help: 列出可用目标
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## //' | awk -F': ' '{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'
