GOTOOLCHAIN ?= local
export GOTOOLCHAIN

.PHONY: build test race vet check run docker-build docker-run tidy

build:
	go build ./...

test:
	go test ./... -count=1

race:
	go test -race ./... -count=1

vet:
	go vet ./...

check: build vet test race

run:
	go run ./cmd/server

tidy:
	go mod tidy

docker-build:
	docker build -t cutvideo:local .

docker-run:
	docker run --rm -p 8080:8080 cutvideo:local
