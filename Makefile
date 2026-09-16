BINARY=tx-submit-api-mirror

ROOT_DIR=$(shell dirname $(realpath $(firstword $(MAKEFILE_LIST))))

GO_FILES=$(shell find $(ROOT_DIR) -name '*.go')

GO_LDFLAGS=-ldflags "-s -w"

.PHONY: build image mod-tidy

build: $(BINARY)

$(BINARY): mod-tidy $(GO_FILES)
	CGO_ENABLED=0 go build \
		$(GO_LDFLAGS) \
		-o $(BINARY) \
		./cmd/$(BINARY)

mod-tidy:
	go mod tidy

clean:
	rm -f $(BINARY)

format: mod-tidy
	go fmt ./...
	gofmt -s -w $(GO_FILES)

golines:
	golines -w --ignore-generated --chain-split-dots --max-len=80 --reformat-tags .

test:
	go test -v ./...

image: build
	docker build -t $(BINARY) .
