.PHONY: build
build:
	CGO_ENABLED=0 go build -o famifo-proto .

.PHONY: vet
vet:
	go vet ./...

.PHONY: unit-test
unit-test:
	go test -coverprofile cover-ut.out -shuffle=on ./...

.PHONY: unit-test-race
unit-test-race:
	go test -coverprofile cover-ut.out -shuffle=on -race ./...

.PHONY: browser-test
browser-test:
	FAMIFO_BROWSER_TESTS=required go test -shuffle=on -tags browser ./internal/web/
