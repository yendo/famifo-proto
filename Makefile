.PHONY: build
build:
	CGO_ENABLED=0 go build -o famifo-proto .

.PHONY: vet
vet:
	go vet ./...

.PHONY: unit-test
unit-test:
	go test -cover ./...

.PHONY: browser-test
browser-test:
	FAMIFO_BROWSER_TESTS=required go test -tags browser ./internal/web/
