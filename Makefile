.PHONY: build run

APP_NAME = proxypool

build:
	cd main && go build -o $(APP_NAME) .

run: build
	cd main && ./$(APP_NAME)
