.PHONY: build run clean

ifeq ($(OS),Windows_NT)
SHELL := C:/Program Files/Git/bin/sh.exe
PATH := C:/Program Files/Git/bin;C:/Program Files/Git/usr/bin;$(PATH)
export PATH
endif

APP_NAME = proxypool
GOOS ?= $(shell go env GOOS)
ifeq ($(GOOS),windows)
EXE = .exe
else
EXE =
endif

VERSION ?= $(shell \
  tag=$$(git describe --tags --abbrev=0 2>/dev/null); \
  if [ -z "$$tag" ]; then echo "dev"; \
  elif git describe --tags --exact-match 2>/dev/null > /dev/null 2>&1; then echo "$$tag"; \
  else echo "$$tag-dev"; fi \
)
BUILD_TIME ?= $(shell TZ=Asia/Shanghai date '+%Y-%m-%dT%H:%M:%S+08:00')
LD_FLAGS = -X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME)

build:
	@echo "==> 目标平台: $(GOOS), 产物: $(APP_NAME)$(EXE)"
	@echo "==> 构建信息: version=$(VERSION) build_time=$(BUILD_TIME)"
	@echo "==> 开始编译 $(APP_NAME) ..."
	@cd main && go build -v -ldflags "$(LD_FLAGS)" -o $(APP_NAME)$(EXE) .
	@echo "==> 编译完成: main/$(APP_NAME)$(EXE)"

run: build
	@echo "==> 启动 $(APP_NAME)$(EXE) --port=1080 --conf=clash.yaml ..."
	cd main && ./$(APP_NAME)$(EXE) --port=1080 --conf=clash.yaml

clean:
	rm -f main/$(APP_NAME) main/$(APP_NAME).exe
	@echo "==> 清理完成"
