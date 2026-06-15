.PHONY: all build gateway simulator test tidy clean lint vet install-deps run run-simulator docker-build docker-up

APP_NAME := oil-tank-radar-gateway
VERSION := 1.0.0
BUILD_DIR := build
CMD_DIR := cmd

GOPATH ?= 
GOBIN ?= /bin
GOFLAGS ?= -tags cgo
LDFLAGS := -ldflags "-X main.version= -X main.buildTime= -w -s"
CGO_CFLAGS := -O3 -ffast-math

all: tidy build

install-deps:
@echo "Installing dependencies..."
go mod download
@echo "Installing tools..."
go install golang.org/x/lint/golint@latest
go install honnef.co/go/tools/cmd/staticcheck@latest
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

tidy:
@echo "Tidying modules..."
CGO_ENABLED=1 go mod tidy

build: gateway simulator

gateway:
@echo "Building gateway..."
@mkdir -p 
CGO_ENABLED=1 CGO_CFLAGS="" go build   -o /gateway.exe .//gateway
@echo "Gateway built successfully: /gateway.exe"

simulator:
@echo "Building simulator..."
@mkdir -p 
CGO_ENABLED=1 go build  -o /simulator.exe .//simulator
@echo "Simulator built successfully: /simulator.exe"

test:
@echo "Running tests..."
CGO_ENABLED=1 go test  -v ./...

test-cover:
@echo "Running tests with coverage..."
CGO_ENABLED=1 go test  -coverprofile=coverage.out -v ./...
go tool cover -html=coverage.out -o coverage.html

bench:
@echo "Running benchmarks..."
CGO_ENABLED=1 go test  -bench=. -benchmem ./...

lint:
@echo "Running linter..."
@if command -v golangci-lint > /dev/null 2>&1; then \
golangci-lint run ./...; \
else \
@echo "golangci-lint not found, using go vet instead"; \
go vet ./...; \
fi

vet:
@echo "Running go vet..."
go vet ./...

staticcheck:
@echo "Running staticcheck..."
@if command -v staticcheck > /dev/null 2>&1; then \
staticcheck ./...; \
else \
@echo "staticcheck not found, skipping"; \
fi

fmt:
@echo "Formatting code..."
go fmt ./...

run: build
@echo "Running gateway..."
.//gateway.exe -config configs/gateway.yaml -log-level debug

run-simulator: build
@echo "Running simulator..."
.//simulator.exe -host 127.0.0.1 -port 9000 -sample-rate 25 -target-level 10.0 -noise 0.02

docker-build:
@echo "Building Docker images..."
docker-compose build

docker-up:
@echo "Starting Docker containers..."
docker-compose up -d

docker-down:
@echo "Stopping Docker containers..."
docker-compose down

clean:
@echo "Cleaning..."
rm -rf 
rm -f coverage.out coverage.html
go clean -cache

distclean: clean
@echo "Deep cleaning..."
go clean -modcache
rm -rf vendor

install: build
@echo "Installing..."
install -d 
install -m 755 /gateway.exe /oil-tank-radar-gateway
install -m 755 /simulator.exe /oil-tank-radar-simulator

version:
@echo "Version: "
@echo "Go version: "

help:
@echo "Available targets:"
@echo "  all              - Default target: tidy and build"
@echo "  install-deps     - Install dependencies and tools"
@echo "  tidy             - Run go mod tidy"
@echo "  build            - Build all binaries"
@echo "  gateway          - Build gateway binary"
@echo "  simulator        - Build simulator binary"
@echo "  test             - Run all tests"
@echo "  test-cover       - Run tests with coverage"
@echo "  bench            - Run benchmarks"
@echo "  lint             - Run linter"
@echo "  vet              - Run go vet"
@echo "  staticcheck      - Run staticcheck"
@echo "  fmt              - Format code"
@echo "  run              - Build and run gateway"
@echo "  run-simulator    - Build and run simulator"
@echo "  docker-build     - Build Docker images"
@echo "  docker-up        - Start Docker containers"
@echo "  docker-down      - Stop Docker containers"
@echo "  clean            - Clean build artifacts"
@echo "  distclean        - Deep clean including module cache"
@echo "  install          - Install binaries to GOPATH/bin"
@echo "  version          - Show version information"
@echo "  help             - Show this help message"

.DEFAULT_GOAL := all