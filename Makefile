APP        := agentflow
CMD        := ./cmd/agentflow
DIST       := dist
VERSION    := v0.4.1
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_TIME := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')

LDFLAGS := -s -w \
  -X github.com/chenhg5/agentflow/internal/version.Version=$(VERSION) \
  -X github.com/chenhg5/agentflow/internal/version.Commit=$(COMMIT) \
  -X github.com/chenhg5/agentflow/internal/version.BuildTime=$(BUILD_TIME)

PLATFORMS := \
  darwin/arm64 \
  darwin/amd64 \
  linux/amd64 \
  linux/arm64 \
  windows/amd64 \
  windows/arm64

SHA256 := $(shell command -v sha256sum >/dev/null 2>&1 && echo sha256sum || echo "shasum -a 256")

.PHONY: build test test-real test-e2e test-e2e-triggers test-benchmark vet check release-preflight release-all clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(APP) $(CMD)

test:
	go test -count=1 ./...

test-real: build
	./scripts/real-agent-smoke.sh

test-e2e: build
	./scripts/e2e-contract-smoke.sh
	./scripts/e2e-execution-smoke.sh
	./scripts/e2e-trigger-smoke.sh
	AGENTFLOW_BIN="$(CURDIR)/bin/$(APP)" python3 scripts/test-durable-queue.py
	AGENTFLOW_BIN="$(CURDIR)/bin/$(APP)" python3 scripts/test-resource-admission.py

test-e2e-triggers: build
	./scripts/e2e-trigger-smoke.sh

test-benchmark: build
	python3 benchmarks/workflow-authoring/selftest.py
	python3 benchmarks/workspace-authoring/suite.py selftest
	python3 benchmarks/workspace-authoring/migration_selftest.py

vet:
	test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	go vet ./...

check: vet test

release-preflight:
	@test -z "$$(git status --porcelain)" || { echo "Commit workspace changes before packaging a release." >&2; exit 1; }

release-all: release-preflight clean check
	@mkdir -p $(DIST)
	@$(foreach platform,$(PLATFORMS), \
		$(eval GOOS   := $(word 1,$(subst /, ,$(platform)))) \
		$(eval GOARCH := $(word 2,$(subst /, ,$(platform)))) \
		$(eval EXT    := $(if $(filter windows,$(GOOS)),.exe,)) \
		$(eval NAME   := $(APP)-$(VERSION)-$(GOOS)-$(GOARCH)) \
		echo "Building $(NAME)" && \
		mkdir -p $(DIST)/$(NAME) && \
		GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/$(NAME)/$(APP)$(EXT) $(CMD) && \
		git archive HEAD README.md INSTALL.md executors.example.json workflow.schema.json trigger.schema.json executors.schema.json resources.schema.json examples skills docs scripts benchmarks changelogs | tar -x -C $(DIST)/$(NAME) && \
		$(if $(EXT),(cd $(DIST) && zip -Xqr $(NAME).zip $(NAME)),COPYFILE_DISABLE=1 tar -C $(DIST) -czf $(DIST)/$(NAME).tar.gz $(NAME)) && \
		rm -rf $(DIST)/$(NAME) && \
	) true
	@cd $(DIST) && $(SHA256) *.tar.gz *.zip > checksums.txt
	@echo "Done: $(DIST)/"; ls -1 $(DIST)

clean:
	rm -rf $(DIST)
