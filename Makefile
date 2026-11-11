BINARY := attention-tui
PREFIX ?= $(HOME)/.local

.PHONY: build test vet install

build:
	go build -o $(BINARY) ./cmd/attention-tui

test:
	go test ./...

vet:
	go vet ./...

install: build
	install -Dm755 $(BINARY) $(DESTDIR)$(PREFIX)/bin/$(BINARY)
