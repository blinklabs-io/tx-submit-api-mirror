BINARY=tx-submit-api-mirror

# Determine root directory
ROOT_DIR=$(shell dirname $(realpath $(firstword $(MAKEFILE_LIST))))

# Gather all .go files for use in dependencies below
GO_FILES=$(shell find $(ROOT_DIR) -name '*.go')

GO_LDFLAGS=-ldflags "-s -w"

.PHONY: build image mod-tidy

# Alias for building program binary
build: $(BINARY)

# Build our program binary
# Depends on GO_FILES to determine when rebuild is needed
$(BINARY): mod-tidy $(GO_FILES)
	CGO_ENABLED=0 go build \
		$(GO_LDFLAGS) \
		-o $(BINARY) \
		./cmd/$(BINARY)

mod-tidy:
	go mod tidy

clean:
	rm -f $(BINARY)

format:
	go fmt ./...

golines:
	golines -w --ignore-generated --chain-split-dots --max-len=80 --reformat-tags .

test:
	go test -v ./...

# Build docker image
image: build
	docker build -t $(BINARY) .
