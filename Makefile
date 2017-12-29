

.PHONY: build run ALL

ALL: build

build:
	go build -o bin/syncd-go.exe

run:
	go run bin/syncd-go.exe

vendor:
	go vendor add github.com/...
	go vendor add golang.org/x/...
