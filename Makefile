.PHONY: web build build-all test test-e2e bench lint clean

BUILD_DIR := dist
BINARY    := polyflow
VERSION   := $(shell grep 'Version' internal/meta/meta.go | head -1 | cut -d'"' -f2)

web:
	cd web && npm install && npm run build

build: web
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/$(BINARY) ./cmd/polyflow

test:
	go test ./... -coverprofile=coverage.out
	go tool cover -func=coverage.out

test-e2e:
	go test ./internal/e2e/... -v -count=1

bench:
	go test ./... -bench=. -benchtime=5s -run=^$

lint:
	golangci-lint run ./...

clean:
	rm -rf $(BUILD_DIR) web/dist coverage.out

PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

build-all:
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		echo "Building $$os/$$arch..."; \
		CGO_ENABLED=1 \
		CC="zig cc -target $$(./scripts/zig_target.sh $$os $$arch)" \
		GOOS=$$os GOARCH=$$arch \
		go build -o $(BUILD_DIR)/$(BINARY)-$$os-$$arch ./cmd/polyflow; \
	done
