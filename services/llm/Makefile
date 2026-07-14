.PHONY: build vet test test-integration

build:
	go build ./...

vet:
	go vet ./...

# Unit tests only
test:
	go test ./...

# Integration tests
test-integration:
	go test -tags=integration ./...
