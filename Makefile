.PHONY: test check run

test:
	go test -race ./...

check: test
	go vet ./...
	go build -o /tmp/paymenthub-check ./cmd/server

run:
	go run ./cmd/server
