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

.PHONY: all build test test-race vet fmt tidy run print-config check-config contract clean web help

all: build

## build: 编译主二进制到 bin/freerouter（版本信息由 ldflags 注入）
build:
	@mkdir -p bin
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/freerouter
	@echo "已构建 $(BINARY)：$(VERSION) ($(COMMIT))"

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

## clean: 清理构建产物与缓存
clean:
	rm -rf bin dist
	rm -rf .cache

## help: 列出可用目标
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## //' | awk -F': ' '{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'
