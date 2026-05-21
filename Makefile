BIN := bin/claude-watch
VERSION ?= dev

LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/claude-watch

clean:
	rm -rf bin
