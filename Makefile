VERSION ?= 0.5.0
DIST := dist/tailscale-multitail-feasibility_$(VERSION)_linux_amd64

.PHONY: test release

test:
	go test ./...

release:
	mkdir -p dist
	GOOS=linux GOARCH=amd64 go build -buildvcs=true -ldflags '-s -w -X main.version=$(VERSION)' -o $(DIST) ./cmd/tailscale-multitail-feasibility
	sha256sum $(DIST) > $(DIST).sha256
