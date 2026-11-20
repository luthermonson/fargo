MODULE    := github.com/luthermonson/rewind
BIN       := rewind
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X $(MODULE)/pkg/version.Version=$(VERSION)
GOFLAGS   := -trimpath
IMAGE     ?= ghcr.io/luthermonson/rewind:dev
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: build test vet lint cross docker-build helm-lint clean install tidy check

build: ## build ./bin/rewind for the host (static)
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o bin/$(BIN) ./cmd/rewind

install:
	CGO_ENABLED=0 go install $(GOFLAGS) -ldflags '$(LDFLAGS)' ./cmd/rewind

test: ## unit tests with the race detector
	go test -race ./...

vet:
	go vet ./...

lint: vet ## gofmt check + vet
	@unformatted=$$(gofmt -l $$(find . -name '*.go' -not -path './vendor/*')); \
	if [ -n "$$unformatted" ]; then echo "gofmt needed on:"; echo "$$unformatted"; exit 1; fi

tidy:
	go mod tidy

check: lint test build ## everything CI runs

# Cross-compile every target into dist/. Static, one self-contained file each.
cross: ## cross-compile for linux/darwin/windows x amd64/arm64
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; ext=""; \
	  [ "$$os" = "windows" ] && ext=".exe"; \
	  out=dist/$(BIN)_$(VERSION)_$${os}_$${arch}$$ext; \
	  echo "  $$out"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $$out ./cmd/rewind || exit 1; \
	done

docker-build: ## build the container image locally (does not push)
	docker build -t $(IMAGE) --build-arg VERSION=$(VERSION) .

helm-lint: ## lint and template the chart for each mode
	helm lint charts/rewind
	helm template rewind charts/rewind --set mode=argo >/dev/null
	helm template rewind charts/rewind --set mode=flux >/dev/null
	helm template rewind charts/rewind --set mode=crd  >/dev/null

clean:
	rm -rf bin dist
