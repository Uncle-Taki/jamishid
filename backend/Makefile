.PHONY: all build test clean run dev

AIR_VERSION := v1.62.0

all: build test

build:
	go build -o bin/jamshid-backend ./cmd/api/main.go

test:
	go test -v -race -coverprofile=coverage.out ./internal/...
	go tool cover -func=coverage.out
	rm -f coverage.out

clean:
	rm -rf bin/

run:
	go run ./cmd/api/main.go

dev:
	go run github.com/air-verse/air@$(AIR_VERSION) -c .air.toml
