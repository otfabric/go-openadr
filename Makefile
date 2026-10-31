# HELP
# This will output the help for each task
# thanks to https://marmelab.com/blog/2016/02/29/auto-documented-makefile.html

.DEFAULT_GOAL := help

.PHONY: help all build examples fmt fmt-check lint lint-ci vet vuln test coverage coverage-pkg-check cover fuzz check clean interop test-interop e2e chaos nodeps storage coverage-check cross bench
help: ## This help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# Parent otfabric/go.work does not list this module; isolate toolchain commands.
export GOWORK := off

# CI formats and lints with the Go version from go.mod, not with the toolchain installed here.
# gofmt's output changes between Go releases, so formatting is done with that version, and
# `fmt-check` also requires the local gofmt to agree.
GO_MOD_VERSION := $(shell sed -nE 's/^go ([0-9]+\.[0-9]+).*/\1/p' go.mod | head -n1)
GOFMT_CI = $(shell GOTOOLCHAIN=go$(GO_MOD_VERSION).0 go env GOROOT)/bin/gofmt

# Output directory for generated binaries
BIN_DIR := bin
# Library packages: tests and coverage (exclude examples and test helpers)
TEST_PKGS := $(shell go list ./... | grep -v '/examples' | grep -v '/interop$$' | grep -v '/interop/')
# Coverage is measured over the library packages, whichever package's tests exercise them:
# the end-to-end tests in the module root drive the client and the server.
COVER_PKGS := $(shell go list ./... | grep -v '/examples' | grep -v '/e2e$$' | grep -v '/internal/mqtttest' | grep -v '/internal/leakcheck' | grep -v '/interop$$' | grep -v '/interop/' | paste -sd, -)

all: build ## Default target: build the examples

build: ## Build the examples into ./bin
	@echo "Building examples"
	@mkdir -p $(BIN_DIR)
	@for dir in examples/*/; do \
		[ -f "$$dir/go.mod" ] && continue; \
		name="$$(basename "$$dir")"; \
		go build -o "$(BIN_DIR)/example-$$name" "./$$dir"; \
	done

# The self-contained examples: each starts both sides and exits by itself.
RUN_EXAMPLES := quickstart notifications prices websocket resourcegroups discovery observability openadr301

examples: ## Run the self-contained examples (each must exit successfully)
	@for e in $(RUN_EXAMPLES); do \
		echo "Running example $$e"; \
		go run ./examples/$$e >/dev/null || { echo "example $$e failed"; exit 1; }; \
	done

fmt: ## Format Go code with the gofmt of the Go version in go.mod (what CI uses)
	@echo "Running gofmt (go$(GO_MOD_VERSION))"
	@$(GOFMT_CI) -w .

fmt-check: ## Fail if the go.mod-version gofmt or the local gofmt would change any file
	@echo "Checking formatting (gofmt go$(GO_MOD_VERSION) and local $$(go env GOVERSION))"
	@ci="$$($(GOFMT_CI) -l .)"; loc="$$(gofmt -l .)"; \
	if [ -n "$$ci$$loc" ]; then \
		[ -z "$$ci" ] || { echo "Not formatted for gofmt go$(GO_MOD_VERSION) (CI):"; echo "$$ci"; }; \
		[ -z "$$loc" ] || { echo "Not formatted for the local gofmt:"; echo "$$loc"; }; \
		echo "Files must be stable under both. Run 'make fmt'."; \
		exit 1; \
	fi

lint: ## Run staticcheck (includes -tags=interop for ./interop)
	@echo "Running staticcheck"
	@staticcheck -tags=interop ./...

lint-ci: ## Run golangci-lint (see .golangci.yml)
	@echo "Running golangci-lint"
	@golangci-lint run ./...

vet: ## Run go vet
	@echo "Running go vet"
	@go vet -tags=interop ./...

vuln: ## Run govulncheck
	@echo "Running govulncheck"
	@govulncheck ./...

e2e: ## Run the end-to-end tests of this module's clients against this module's VTN
	@echo "Running end-to-end tests"
	@go test -count=1 -race ./e2e/...

# How often 'make chaos' repeats its tests. Each run has a seed of its own; a
# failing test prints the one to repeat it with (OPENADR_CHAOS_SEED=...).
CHAOS_COUNT ?= 5
chaos: ## Run the parallel and failure-injection tests CHAOS_COUNT times (default 5)
	@echo "Running the parallel and chaos tests $(CHAOS_COUNT) times"
	@go test -count=$(CHAOS_COUNT) -race -timeout=30m -run='^(TestParallel|TestChaos)' ./e2e/...

nodeps: ## Fail if the module requires anything outside the standard library
	@echo "Checking that the module has no dependencies"
	@deps="$$(go list -m all | tail -n +2)"; \
	if [ -n "$$deps" ]; then echo "go.mod requires:"; echo "$$deps"; exit 1; fi

# examples/storage is a module of its own: it has the SQLite and PostgreSQL
# drivers this module must not depend on, and needs a newer Go for them.
# PostgreSQL runs in Docker, or is the server named by OPENADR_POSTGRES_DSN;
# OPENADR_STORAGE_REQUIRE=1 makes its absence a failure instead of a skip.
storage: ## Test the SQLite and PostgreSQL example stores (PostgreSQL needs Docker)
	@echo "Testing the example stores"
	@cd examples/storage && go vet ./... && go test -count=1 -race ./... && go build -o /dev/null ./vtn

test: ## Run the library tests with the race detector
	@echo "Running tests"
	@go test -count=1 -race $(TEST_PKGS)

coverage: ## Run tests with coverage on the library packages (writes coverage.out)
	@echo "Running coverage"
	@go test -count=1 -race -coverprofile=coverage.out -covermode=atomic -coverpkg=$(COVER_PKGS) $(TEST_PKGS)
	@go tool cover -func=coverage.out | tail -n1

cover: coverage ## Open coverage report in browser
	@echo "Opening coverage report"
	@go tool cover -html=coverage.out

# Duration each fuzz target runs. Override with FUZZTIME=... (e.g. FUZZTIME=5m).
# Same minimum as coverage-min in ci.yml.
COVER_MIN := 91
# Per-package floor (TEST-012), over the packages of COVER_PKGS.
COVER_PKG_MIN := 80
FUZZTIME ?= 30s
fuzz: ## Run every fuzz target for FUZZTIME (default 30s; targets discovered, never listed)
	@echo "Fuzzing (FUZZTIME=$(FUZZTIME))"
	@for pkg in $(TEST_PKGS); do \
		for t in $$(go test -list '^Fuzz' $$pkg | grep '^Fuzz'); do \
			echo "fuzz $$pkg $$t"; \
			go test -run '^$$' -fuzz "^$$t$$" -fuzztime=$(FUZZTIME) $$pkg || exit 1; \
		done; \
	done

# Reference implementations for 'make interop': otfabric/openadr-interop v0.5.0
# (the OpenLEADR VTN v0.2.7 with a Mosquitto broker, the ElaadNL
# openadr3-client 2.4.0, and OpenLEADR v0.1.4 for OpenADR 3.0.1), pinned by
# multi-arch index digest.
# Keep in step with interop/harness_test.go and .github/workflows/interop.yml.
# The suite also runs this module's MQTT publisher and subscriber on four
# brokers (Mosquitto, NanoMQ, HiveMQ CE, EMQX), whose images are pinned in
# interop/harness_test.go and pulled from Docker Hub when they are not there.
# A reference image that cannot be had fails the run: asked to test against it,
# passing by skipping would say nothing.
# Override to test other builds, e.g. the local ones of a checkout of that repository:
#   make interop OPENADR_INTEROP_OPENLEADR_IMAGE=ghcr.io/otfabric/openadr-interop-openleadr:dev
OPENADR_INTEROP_OPENLEADR_IMAGE ?= ghcr.io/otfabric/openadr-interop-openleadr@sha256:4f8eb4b1795bd3f21307346eb8cff0e3860f9c9753e841710de50a5af1353ab5
OPENADR_INTEROP_ELAADNL_IMAGE   ?= ghcr.io/otfabric/openadr-interop-elaadnl@sha256:3c8d2e56b87740f138d3e083303c8333a28cfe6d386aab1a9a009ae70c33d12f
OPENADR_INTEROP_OPENLEADR301_IMAGE ?= ghcr.io/otfabric/openadr-interop-openleadr301@sha256:a2039fff5b7d3e4a3691883a543bb602b885a07ed8a3a2779fed238627c7e1a5

test-interop: ## Run the interop suite: OpenLEADR (3.1.0 and 3.0.1) and ElaadNL reference images, and four MQTT brokers (needs Docker)
	@echo "Running interop tests against $(OPENADR_INTEROP_OPENLEADR_IMAGE), $(OPENADR_INTEROP_ELAADNL_IMAGE) and $(OPENADR_INTEROP_OPENLEADR301_IMAGE)"
	@OPENADR_INTEROP_OPENLEADR_IMAGE="$(OPENADR_INTEROP_OPENLEADR_IMAGE)" \
		OPENADR_INTEROP_ELAADNL_IMAGE="$(OPENADR_INTEROP_ELAADNL_IMAGE)" \
		OPENADR_INTEROP_OPENLEADR301_IMAGE="$(OPENADR_INTEROP_OPENLEADR301_IMAGE)" \
		OPENADR_INTEROP_REQUIRE=1 \
		go test -tags=interop -count=1 -timeout=20m ./interop/...

interop: test-interop ## Alias for test-interop

coverage-pkg-check: coverage ## Fail when a package of COVER_PKGS is below COVER_PKG_MIN (reads coverage.out)
	@awk -v m="$(COVER_PKG_MIN)" -v pkgs="$(COVER_PKGS)" 'BEGIN { split(pkgs, l, ","); for (i in l) want[l[i]] = 1 } \
	NR > 1 { n[$$1] = $$2; if ($$3 > 0) h[$$1] = 1 } \
	END { for (k in n) { p = k; sub(/\/[^\/]*$$/, "", p); if (p in want) { s[p] += n[k]; if (k in h) c[p] += n[k] } } \
	for (p in s) { v = 100 * c[p] / s[p]; if (v < m) { printf "%s: %.1f%% < %s%%\n", p, v, m; bad = 1 } } exit bad }' coverage.out

coverage-check: coverage ## Fail when total coverage is below COVER_MIN
	@total=$$(go tool cover -func=coverage.out | awk '/^total:/ {sub("%","",$$3); print $$3}'); \
	awk -v t="$$total" -v m="$(COVER_MIN)" 'BEGIN { if (t+0 < m+0) { printf "coverage %.1f%% < %s%%\n", t, m; exit 1 } else printf "coverage %.1f%% >= %s%%\n", t, m }'

CROSS_PLATFORMS := linux/amd64 linux/arm64 linux/arm darwin/arm64 windows/amd64

cross: ## Cross-compile without cgo for the supported platforms
	@for p in $(CROSS_PLATFORMS); do \
		echo "Building for $$p"; \
		CGO_ENABLED=0 GOOS=$${p%/*} GOARCH=$${p#*/} go build ./... || exit 1; \
	done

bench: ## Benchmarks
	@go test -count=1 -run='^$$' -bench=. -benchmem $(TEST_PKGS)

check: fmt fmt-check nodeps lint lint-ci vet vuln test coverage coverage-pkg-check examples ## Run format + lint + vet + vuln + test

clean: ## Remove generated binaries and coverage artifacts
	@echo "Cleaning up"
	@rm -rf $(BIN_DIR) coverage.out coverage.html
