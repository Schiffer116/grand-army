.DEFAULT_GOAL := help
.PHONY: help build test e2e lint fmt check hooks restart status log clean

help: ## List targets
	@grep -E '^[a-zA-Z0-9_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-8s %s\n", $$1, $$2}'

build: ## Build bin/berthier
	go build -o bin/berthier ./cmd/berthier

test: ## Unit tests (go test ./...)
	go test ./...

e2e: ## E2E against a real Herdr and Claude haiku (~1 min)
	tests/e2e.sh

lint: ## go vet, and fail if gofmt has changes
	go vet ./...
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

fmt: ## gofmt -w
	gofmt -w .

check: lint test ## Pre-commit gate: lint + test

hooks: ## Install .githooks as core.hooksPath
	git config core.hooksPath .githooks

restart: ## Restart the loop and Console services
	systemctl --user restart berthier-loop berthier-dashboard

status: ## Show the loop and Console services
	systemctl --user status berthier-loop berthier-dashboard --no-pager

log: build ## Follow the loop log
	bin/berthier log -f

clean: ## Remove built binaries
	rm -f bin/berthier main
