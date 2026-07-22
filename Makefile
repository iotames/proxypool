.PHONY: build run

APP_NAME = proxypool
VERSION ?= $(shell \
  tag=$$(git describe --tags --abbrev=0 2>/dev/null); \
  if [ -z "$$tag" ]; then echo "dev"; \
  elif git describe --tags --exact-match 2>/dev/null > /dev/null 2>&1; then echo "$$tag"; \
  else echo "$$tag-dev"; fi \
)
BUILD_TIME ?= $(shell date -u '+%Y-%m-%dT%H:%M:%S+08:00' -d '+8 hours')
LD_FLAGS = -X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME)

build:
	cd main && go build -ldflags "$(LD_FLAGS)" -o $(APP_NAME) .

run: build
	cd main && ./$(APP_NAME)
