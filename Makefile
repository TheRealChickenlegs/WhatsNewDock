SHELL := /bin/bash

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BINARY  := bin/whatsnewdock
WEB_DIR := web
EMBED_DIR := internal/webui/dist

.PHONY: all build build-web build-go test lint fmt clean docker

all: build

## Build the frontend and embed it into the Go binary.
build: build-web build-go

## Build the web UI (requires Node 20+).
build-web:
	cd $(WEB_DIR) && npm ci && npm run build
	rm -rf $(EMBED_DIR)
	cp -r $(WEB_DIR)/dist $(EMBED_DIR)

## Build the Go binary (embed the already-built web assets).
build-go:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BINARY) ./cmd/whatsnewdock

## Run Go tests and the frontend typecheck.
test:
	go test ./...
	cd $(WEB_DIR) && npm run typecheck

## Vet and format-check the Go code.
lint:
	go vet ./...
	gofmt -l .

fmt:
	gofmt -w .

## Build the container image locally.
docker:
	docker build -t whatsnewdock:$(VERSION) .

clean:
	rm -rf $(BINARY) $(WEB_DIR)/dist $(EMBED_DIR)/*
