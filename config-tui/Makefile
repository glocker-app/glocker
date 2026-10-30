.DEFAULT_GOAL:=help
help: ## display this help message
	@echo "Please use \`make <target>' where <target> is one of"
	@perl -nle'print $& if m{^[a-zA-Z_-]+:.*?## .*$$}' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m  %-25s\033[0m %s\n", $$1, $$2}'

CONF ?= ../conf/conf.yaml

build: ## build ./config-tui
	go build -o config-tui .

test: ## run the tests
	go test ./...

run: build ## edit $(CONF), writing <file>.new next to each changed file
	./config-tui $(CONF)

.PHONY: help build test run
