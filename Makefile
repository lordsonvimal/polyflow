.PHONY: web build build-all test test-e2e bench lint clean

BUILD_DIR := dist
BINARY    := polyflow
VERSION   := $(shell grep 'Version' internal/meta/meta.go | head -1 | cut -d'"' -f2)

web:
	cd web && npm install && npm run build
	# vite empties dist on build; restore the committed embed placeholder so
	# `//go:embed all:dist` stays satisfied and the working tree stays clean.
	touch web/dist/.gitkeep

build: web
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/$(BINARY) ./cmd/polyflow
	# V.2 parser sidecar; discovered next to the polyflow binary at runtime.
	CGO_ENABLED=1 go build -o $(BUILD_DIR)/polyflow-parse-templ ./cmd/polyflow-parse-templ

test:
	go test ./... -coverprofile=coverage.out
	go tool cover -func=coverage.out

test-e2e:
	go test ./internal/e2e/... -v -count=1

bench:
	go test ./... -bench=. -benchtime=5s -run=^$

# eval-corpus — clone + index all URL-based corpus repos into eval/.cache/
# Skips offline repos with a warning; never silently passes a missing clone.
eval-corpus:
	@mkdir -p eval/.cache
	@POLYFLOW=./dist/polyflow; \
	if [ ! -x "$$POLYFLOW" ]; then \
		echo "error: $$POLYFLOW not found — run 'make build' first"; exit 1; \
	fi; \
	ONLINE=1; \
	if ! curl -sf --max-time 5 https://github.com > /dev/null 2>&1; then \
		ONLINE=0; \
		echo "WARNING: offline — skipping remote repo clones (run again when online)"; \
	fi; \
	for manifest in eval/corpus/*/manifest.yaml; do \
		dir=$$(dirname "$$manifest"); \
		name=$$(basename "$$dir"); \
		url=$$(grep -m1 'url:' "$$manifest" | awk '{print $$2}'); \
		sha=$$(grep -m1 'sha:' "$$manifest" | awk '{print $$2}'); \
		if [ -z "$$url" ]; then continue; fi; \
		cachedir="eval/.cache/$$name"; \
		if [ "$$ONLINE" = "0" ]; then \
			if [ ! -d "$$cachedir/.git" ]; then \
				echo "WARNING: offline and $$cachedir not cloned — eval for $$name will fail"; \
			else \
				echo "$$name: already cloned (offline, skipping update)"; \
			fi; \
			continue; \
		fi; \
		if [ ! -d "$$cachedir/.git" ]; then \
			echo "Cloning $$name from $$url ..."; \
			git clone --quiet "$$url" "$$cachedir"; \
		fi; \
		echo "Pinning $$name to $$sha ..."; \
		git -C "$$cachedir" fetch --quiet origin; \
		git -C "$$cachedir" checkout --quiet "$$sha"; \
		if [ -f "$$dir/workspace.yaml" ]; then \
			cp "$$dir/workspace.yaml" "$$cachedir/workspace.yaml"; \
		fi; \
		echo "Indexing $$name ..."; \
		(cd "$$cachedir" && $$POLYFLOW index --full --workspace workspace.yaml) || echo "WARNING: index failed for $$name"; \
	done; \
	echo "eval-corpus done."

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
