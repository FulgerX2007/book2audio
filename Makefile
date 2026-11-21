GO  ?= go
BIN := bin/book2audio

.PHONY: build test vet race

build:
	$(GO) build -o $(BIN) ./cmd/book2audio

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

race:
	$(GO) test -race ./...
