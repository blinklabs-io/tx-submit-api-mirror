BINARY=tx-submit-api-mirror
GOOS ?= $(shell go env GOOS)
BINARY_SUFFIX := $(if $(filter windows,$(GOOS)),.exe,)
BINARY_OUTPUT := $(BINARY)$(BINARY_SUFFIX)

ROOT_DIR=$(shell dirname $(realpath $(firstword $(MAKEFILE_LIST))))

GO_FILES=$(shell find $(ROOT_DIR) -name '*.go')

# Extract Go module name from go.mod
GOMODULE=$(shell grep ^module $(ROOT_DIR)/go.mod | awk '{ print $$2 }')

# Set version strings based on git tag and current ref
GO_LDFLAGS=-ldflags "-s -w -X '$(GOMODULE)/internal/version.Version=$(shell git describe --tags --exact-match 2>/dev/null)' -X '$(GOMODULE)/internal/version.CommitHash=$(shell git rev-parse --short HEAD)'"

.PHONY: build image mod-tidy

build: $(BINARY_OUTPUT)

$(BINARY_OUTPUT): mod-tidy $(GO_FILES)
	CGO_ENABLED=0 go build \
		$(GO_LDFLAGS) \
		-o $(BINARY_OUTPUT) \
		./cmd/$(BINARY)

mod-tidy:
	go mod tidy

clean:
	rm -f $(BINARY) $(BINARY).exe

format: mod-tidy
	go fmt ./...
	gofmt -s -w $(GO_FILES)

golines:
	golines -w --ignore-generated --chain-split-dots --max-len=80 --reformat-tags .

test:
	go test -v ./...

image: build
	docker build -t $(BINARY) .
