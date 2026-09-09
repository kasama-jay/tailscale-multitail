VERSION ?= 0.5.0
V1_VERSION ?= 1.0.0
DIST := dist/tailscale-multitail-feasibility_$(VERSION)_linux_amd64
V1_DIST := dist/tailscale-multitail_$(V1_VERSION)_linux_amd64

.PHONY: test release release-v1

test:
	go test ./...

release:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=true -ldflags '-s -w -X main.version=$(VERSION)' -o $(DIST) ./cmd/tailscale-multitail-feasibility
	sha256sum $(DIST) > $(DIST).sha256

release-v1:
	mkdir -p $(V1_DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=true -ldflags '-s -w -X main.version=$(V1_VERSION)' -o $(V1_DIST)/tailscale-multitaild ./cmd/tailscale-multitaild
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=true -ldflags '-s -w -X main.version=$(V1_VERSION)' -o $(V1_DIST)/tsmultitail ./cmd/tsmultitail
	cp packaging/systemd/tailscale-multitail.service packaging/install.sh packaging/INSTALL.md README.md $(V1_DIST)/
	chmod 0755 $(V1_DIST)/install.sh
	(cd $(V1_DIST) && sha256sum tailscale-multitaild tsmultitail tailscale-multitail.service install.sh INSTALL.md README.md > SHA256SUMS)
	tar -C dist -czf $(V1_DIST).tar.gz $(notdir $(V1_DIST))
	sha256sum $(V1_DIST).tar.gz > $(V1_DIST).tar.gz.sha256
