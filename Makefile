.PHONY: help init build build-examples run test lint preflight clean \
        refresh-spec refresh-web-deps \
        mock-up mock-down \
        proxy-up proxy-down \
        grafana-up grafana-down \
        debug-chat

BIN_DIR := bin
MOCK_BIN := $(BIN_DIR)/telegym-mock
ECHO_BIN := $(BIN_DIR)/echobot
K6_BIN := $(BIN_DIR)/k6
K6_DEPS := $(wildcard pkg/xk6/*.go) pkg/xk6/go.mod pkg/xk6/go.sum

help: ## Show this help
	@awk 'BEGIN {FS = ":.*?## "} \
	     /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5); next } \
	     /^[a-zA-Z0-9_-]+:.*?## / { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 }' \
	     $(MAKEFILE_LIST)

##@ Setup

init: ## First-time setup: copy .env.example, download deps
	@test -f .env || { cp .env.example .env; echo "created .env from .env.example"; }
	@go mod download
	@cd pkg/xk6 && go mod download
	@echo ""
	@echo "ready. next steps:"
	@echo "  make build         - compile production binaries (mock, proxy)"
	@echo "  make build-examples - compile demo bot + k6 with xk6-telegym"
	@echo "  make mock-up       - start the mock; bring your own bot"
	@echo "  make debug-chat    - open /debug/chat in the browser"

##@ Build

build: ## Build production binaries only (telegym-mock, telegym-proxy)
	@mkdir -p $(BIN_DIR)
	@go build -o $(BIN_DIR)/ ./cmd/...

build-examples: $(K6_BIN) ## Build demo binaries (echobot) and the xk6-telegym k6
	@mkdir -p $(BIN_DIR)
	@go build -o $(BIN_DIR)/ ./examples/...

# k6 file target with mtime-based deps - rebuild is skipped unless pkg/xk6/*
# actually changed, sparing the ~13s xk6 assembly per warm build. Output lives
# in bin/ so the path doesn't collide with a phony `k6` target name.
$(K6_BIN): $(K6_DEPS)
	@mkdir -p $(BIN_DIR)
	@command -v xk6 >/dev/null 2>&1 || go install go.k6.io/xk6/cmd/xk6@latest
	GOWORK=off xk6 build --output $(K6_BIN) \
		--with github.com/kolomiichenko/telegym/pkg/xk6=./pkg/xk6 \
		--replace github.com/kolomiichenko/telegym=$(CURDIR)

run: build ## Run mock server locally (foreground)
	$(MOCK_BIN)

##@ Quality (test + lint + scan)

test: ## Run unit tests (both root module and pkg/xk6)
	go test -v ./...
	cd pkg/xk6 && go test -v ./...

lint: ## Run linters
	go vet ./...
	cd pkg/xk6 && go vet ./...
	gofmt -l -s . | tee /dev/stderr | (! read)

preflight: ## Full local validation - mirrors CI. Run before pushing.
	@echo "=== gofmt ==="
	@test -z "$$(gofmt -l -s . | tee /dev/stderr)" || { echo "gofmt failed - run 'gofmt -s -w .'"; exit 1; }
	@echo "=== go vet (root module) ==="
	@go vet ./...
	@echo "=== go vet (pkg/xk6) ==="
	@cd pkg/xk6 && go vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then \
		echo "=== golangci-lint (root module) ==="; \
		golangci-lint run --timeout=5m ./... || exit 1; \
		echo "=== golangci-lint (pkg/xk6) ==="; \
		(cd pkg/xk6 && golangci-lint run --timeout=5m ./...) || exit 1; \
	else echo "(golangci-lint not installed - skipping. install: brew install golangci-lint)"; fi
	@if command -v actionlint >/dev/null 2>&1; then \
		echo "=== actionlint ==="; \
		actionlint .github/workflows/*.yml || exit 1; \
	else echo "(actionlint not installed - skipping. install: brew install actionlint)"; fi
	@if command -v yamllint >/dev/null 2>&1; then \
		echo "=== yamllint ==="; \
		yamllint -d relaxed . || exit 1; \
	else echo "(yamllint not installed - skipping. install: brew install yamllint)"; fi
	@if command -v govulncheck >/dev/null 2>&1; then \
		echo "=== govulncheck ==="; \
		govulncheck ./... || exit 1; \
	else echo "(govulncheck not installed - skipping. install: go install golang.org/x/vuln/cmd/govulncheck@latest)"; fi
	@if command -v trivy >/dev/null 2>&1; then \
		echo "=== trivy fs ==="; \
		trivy fs --quiet --scanners vuln,secret,misconfig \
			--skip-dirs bin,.git \
			--severity HIGH,CRITICAL --exit-code 1 . || exit 1; \
	else echo "(trivy not installed - skipping. install: brew install trivy)"; fi
	@echo "=== go test (root module) ==="
	@go test -race -timeout 5m ./...
	@echo "=== go test (pkg/xk6) ==="
	@cd pkg/xk6 && go test -race -timeout 5m ./...
	@echo "=== cross-platform build ==="
	@for goos in linux darwin windows; do \
		for goarch in amd64 arm64; do \
			[ "$$goos/$$goarch" = "windows/arm64" ] && continue; \
			printf "  %-15s " "$$goos/$$goarch"; \
			GOOS=$$goos GOARCH=$$goarch CGO_ENABLED=0 go build -o /dev/null ./cmd/... \
				&& echo "OK" || { echo "FAIL"; exit 1; }; \
		done; \
	done
	@echo ""
	@echo "preflight passed - safe to push"

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)

##@ Refresh vendored assets

refresh-spec: ## Pull the latest Bot API spec from PaulSonOfLars/telegram-bot-api-spec
	curl -sSfL https://raw.githubusercontent.com/PaulSonOfLars/telegram-bot-api-spec/main/api.json -o pkg/mock/spec/api.json
	@grep -m1 '"version"\|"release_date"' pkg/mock/spec/api.json | head -2

refresh-web-deps: ## Pull the latest htmx + idiomorph (both 0BSD; embedded into telegym-mock binary)
	curl -sSfL https://unpkg.com/htmx.org@latest/dist/htmx.min.js -o pkg/mock/web/htmx.min.js
	curl -sSfL https://unpkg.com/idiomorph@latest/dist/idiomorph-ext.min.js -o pkg/mock/web/idiomorph-ext.min.js
	@echo "htmx:       $$(wc -c < pkg/mock/web/htmx.min.js) bytes"
	@echo "idiomorph:  $$(wc -c < pkg/mock/web/idiomorph-ext.min.js) bytes"
	@echo "rebuild:    make mock"

# PORT lets you run the mock on something other than :5678 without editing
# the recipes (e.g. `make mock-up PORT=9000`). All up/down targets honor it.
PORT ?= 5678

##@ Stacks (up/down lifecycle)

mock-up: build ## Start the mock in background. Override port with PORT=N.
	@lsof -ti :$(PORT) 2>/dev/null | xargs -r kill -9 2>/dev/null; true
	@sleep 0.3
	@TELEGYM_MOCK_LISTEN=:$(PORT) ./bin/telegym-mock -quiet >/tmp/telegym-mock.log 2>&1 & echo $$! >/tmp/telegym-mock.pid
	@sleep 0.3
	@echo "stop with: make mock-down"

mock-down: ## Stop the mock started by mock-up
	@kill $$(cat /tmp/telegym-mock.pid 2>/dev/null) 2>/dev/null; true
	@rm -f /tmp/telegym-mock.pid
	@echo stopped

proxy-up: build ## Start mock + telegym-proxy (foreground). Your bot must be running and pointed at the mock.
	@test -n "$$PROXY_TOKEN" || { echo "ERROR: export PROXY_TOKEN (from @BotFather) first"; exit 1; }
	@test -n "$$MOCK_BOT_TOKEN" || { echo "ERROR: export MOCK_BOT_TOKEN (the token your bot uses against the mock)"; exit 1; }
	@lsof -ti :$(PORT) :8090 2>/dev/null | xargs -r kill -9 2>/dev/null; true
	@sleep 0.3
	@TELEGYM_MOCK_LISTEN=:$(PORT) ./bin/telegym-mock -quiet >/tmp/telegym-mock.log 2>&1 & echo $$! >/tmp/telegym-mock.pid
	@sleep 0.4
	@echo "proxy starting - make sure your bot is up and pointed at TELEGRAM_API_URL=http://localhost:$(PORT)"
	PROXY_TOKEN=$$PROXY_TOKEN MOCK_URL=http://localhost:$(PORT) MOCK_BOT_TOKEN=$$MOCK_BOT_TOKEN ./bin/telegym-proxy

proxy-down: ## Stop the proxy stack
	@lsof -ti :$(PORT) :8090 2>/dev/null | xargs -r kill -9 2>/dev/null; true
	@rm -f /tmp/telegym-mock.pid
	@echo stopped

grafana-up: ## Start Prometheus + Grafana (use with host-mode telegym-mock)
	docker compose -f docker/docker-compose.yml up -d prometheus grafana
	@echo "Prometheus: http://localhost:9091"
	@echo "Grafana:    http://localhost:3001  (anonymous admin, dashboard telegym)"

grafana-down: ## Stop Prometheus + Grafana
	docker compose -f docker/docker-compose.yml stop prometheus grafana

##@ Browser helpers

debug-chat: ## Open /debug/chat in your browser (mock must already be running)
	@open http://localhost:$(PORT)/debug/chat 2>/dev/null \
		|| echo "open in browser: http://localhost:$(PORT)/debug/chat"

# Demo scenarios are intentionally NOT make targets - they live under
# examples/echobot/ as self-contained samples. Run via:
#     ./examples/echobot/run.sh           # default: echo
#     ./examples/echobot/run.sh echo_pool
