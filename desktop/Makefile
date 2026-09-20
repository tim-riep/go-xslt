# go-xslt desktop GUI — convenience build targets (no external `task` runner
# required). This is a separate Go module (github.com/tim-riep/go-xslt/desktop)
# that consumes the engine library from the repo root via a replace directive.
#
# The Wails-generated Taskfile.yml still works if you install go-task, but these
# targets cover the common flows with plain make + the wails3 CLI.

APP      := go-xslt
BIN      := build/bin
GOBIN    := $(shell go env GOPATH)/bin
WAILS    := $(GOBIN)/wails3
# wails3's own scaffolded Taskfile (build/{windows,darwin,linux}/Taskfile.yml)
# shells out to a bare `wails3` in a few places (e.g. detecting a C compiler
# before routing macOS/Linux builds native-vs-Docker) — that only resolves if
# GOBIN is on PATH, which it often isn't in a plain `make` invocation, so every
# `$(WAILS) task ...` call below runs with it prepended.
TASKPATH := PATH="$(GOBIN):$$PATH"
GO_FILES := $(shell find . -name '*.go' -not -path './frontend/*')

.PHONY: all dev bindings frontend build \
	build-mac build-mac-arm64 build-mac-amd64 \
	build-win build-win-amd64 build-win-arm64 \
	build-linux setup-docker \
	build-all clean test tidy

all: build-all

## dev: run the app with live frontend reload (needs go-task: `go install ...`/brew)
dev:
	$(WAILS) dev

## bindings: regenerate TypeScript bindings from Go services (-ts for real
## .ts output, -i for interfaces instead of classes — frontend/src/lib/api.ts
## re-exports these generated types directly rather than hand-mirroring them)
bindings:
	$(WAILS) generate bindings -ts -i

## frontend: install deps (first run) and build the production bundle into frontend/dist
frontend:
	cd frontend && npm install && npm run build

## build: native build for the host OS (embeds frontend/dist)
build: frontend
	mkdir -p $(BIN)
	go build -o $(BIN)/$(APP) .

## build-mac-arm64: macOS build for Apple Silicon. Native CGo (webkit) via the
## wails3 task pipeline (icon/bindings generation included) — cross-compiles
## cleanly from an Intel Mac too, since Xcode's clang targets both Apple archs.
build-mac-arm64:
	mkdir -p $(BIN)
	$(TASKPATH) $(WAILS) task darwin:build ARCH=arm64 OUTPUT=$(BIN)/$(APP)-darwin-arm64

## build-mac-amd64: macOS build for Intel. Same pipeline, cross-compiles
## cleanly from an Apple Silicon Mac too.
build-mac-amd64:
	mkdir -p $(BIN)
	$(TASKPATH) $(WAILS) task darwin:build ARCH=amd64 OUTPUT=$(BIN)/$(APP)-darwin-amd64

## build-mac: both macOS architectures (Apple Silicon + Intel)
build-mac: build-mac-arm64 build-mac-amd64

## build-win-amd64: Windows x64 (pure Go, no CGo, CGO_ENABLED=0 -H=windowsgui)
build-win-amd64:
	mkdir -p $(BIN)
	$(TASKPATH) $(WAILS) task windows:build ARCH=amd64
	mv bin/$(APP).exe $(BIN)/$(APP)-windows-amd64.exe

## build-win-arm64: Windows ARM64 (pure Go, no CGo, CGO_ENABLED=0 -H=windowsgui)
build-win-arm64:
	mkdir -p $(BIN)
	$(TASKPATH) $(WAILS) task windows:build ARCH=arm64
	mv bin/$(APP).exe $(BIN)/$(APP)-windows-arm64.exe

## build-win: both Windows architectures (x64 + ARM64)
build-win: build-win-amd64 build-win-arm64

## setup-docker: one-time (~800MB download) — builds the wails-cross image
## build-linux needs. Requires Docker Desktop (or another docker daemon).
setup-docker:
	$(WAILS) task setup:docker

## build-linux: Linux x64 via Docker cross-compile. Unlike Windows, Linux needs
## real CGo (webkitgtk) — there's no pure-Go GUI backend for it — and there's
## no macOS-hosted C toolchain that can link against Linux's GTK/WebKitGTK, so
## this always goes through Docker (run `make setup-docker` once first).
build-linux:
	mkdir -p $(BIN)
	$(TASKPATH) $(WAILS) task linux:build ARCH=amd64 OUTPUT=$(BIN)/$(APP)-linux-amd64

## build-all: every target platform — Windows x64 + ARM64, macOS Apple Silicon
## + Intel, Linux x64 — into $(BIN), each arch-suffixed.
build-all: build-win-amd64 build-win-arm64 build-mac-arm64 build-mac-amd64 build-linux

## test: run the desktop module's unit tests (internal/project). The engine +
## conformance tests live in the root library module — run `make test` there.
test:
	go test ./...

tidy:
	go mod tidy

clean:
	rm -rf $(BIN) bin frontend/dist
