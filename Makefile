.PHONY: build
build:
	CGO_ENABLED=0 go build -o famifo-proto .

.PHONY: vet
vet:
	go vet ./...

.PHONY: unit-test
unit-test:
	go test -cover -shuffle=on ./...

.PHONY: unit-test-race
unit-test-race:
	go test -cover -shuffle=on -race ./...

.PHONY: browser-test
browser-test:
	FAMIFO_BROWSER_TESTS=required go test -tags browser ./internal/web/
