VERSION ?= 0.5.0
V1_VERSION ?= 1.0.0
DIST := dist/tailscale-multitail-feasibility_$(VERSION)_linux_amd64
V1_DIST := dist/tailscale-multitail_$(V1_VERSION)_linux_amd64

.PHONY: test release release-v1

test:
	go test ./...

release:
	mkdir -p dist
	GOOS=linux GOARCH=amd64 go build -buildvcs=true -ldflags '-s -w -X main.version=$(VERSION)' -o $(DIST) ./cmd/tailscale-multitail-feasibility
	sha256sum $(DIST) > $(DIST).sha256

release-v1:
	mkdir -p $(V1_DIST)
	GOOS=linux GOARCH=amd64 go build -buildvcs=true -ldflags '-s -w -X main.version=$(V1_VERSION)' -o $(V1_DIST)/tailscale-multitaild ./cmd/tailscale-multitaild
	GOOS=linux GOARCH=amd64 go build -buildvcs=true -ldflags '-s -w -X main.version=$(V1_VERSION)' -o $(V1_DIST)/tsmultitail ./cmd/tsmultitail
	cp packaging/systemd/tailscale-multitail.service $(V1_DIST)/
	(cd $(V1_DIST) && sha256sum tailscale-multitaild tsmultitail tailscale-multitail.service > SHA256SUMS)
