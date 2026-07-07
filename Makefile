.PHONY: build build-all test lint clean

BUILD_DIR := dist
BINARY    := polyflow
VERSION   := $(shell grep 'Version' internal/meta/meta.go | head -1 | cut -d'"' -f2)

build:
	cd web && npm run build && cd ..
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/$(BINARY) ./cmd/polyflow

test:
	go test ./... -coverprofile=coverage.out
	go tool cover -func=coverage.out

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
