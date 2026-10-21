.PHONY: all build test clean run

all: build test

build:
	go build -o bin/jamshid-backend ./cmd/api/main.go

test:
	go test ./...

clean:
	rm -rf bin/

run:
	go run ./cmd/api/main.go
