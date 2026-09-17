package main

import (
	"bytes"
	"net/netip"
	"strings"
	"testing"

	"github.com/jay/tailscale-multitail/internal/inventory"
)

func mustAddr(value string) netip.Addr {
	return netip.MustParseAddr(value)
}

func TestWriteStatusTable(t *testing.T) {
	var out bytes.Buffer
	writeStatusTable(&out, []inventory.Target{
		{
			ProfileName: "work",
			FQDN:        "long-host.example.ts.net.",
			CanonicalIP: mustAddr("100.100.100.100"),
			Kind:        inventory.Node,
			Online:      true,
		},
		{
			ProfileName: "home",
			FQDN:        "svc.example.ts.net.",
			CanonicalIP: mustAddr("100.1.2.3"),
			Kind:        inventory.Service,
		},
	})

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines:\n%s", len(lines), out.String())
	}

	if strings.Contains(out.String(), "\t") {
		t.Fatalf("table contains tabs:\n%s", out.String())
	}

	fqdnColumn := strings.Index(lines[0], "FQDN")
	ipColumn := strings.Index(lines[0], "CANONICAL_IP")
	onlineColumn := strings.Index(lines[0], "ONLINE")
	for _, line := range lines[1:] {
		if len(line) <= onlineColumn {
			t.Fatalf("short table line %q", line)
		}
		if strings.Index(line, ".") < fqdnColumn || strings.Index(line, "100.") < ipColumn {
			t.Fatalf("misaligned table line %q", line)
		}
	}

	if !strings.HasSuffix(strings.TrimSpace(lines[1]), "true") || !strings.HasSuffix(strings.TrimSpace(lines[2]), "-") {
		t.Fatalf("unexpected online values:\n%s", out.String())
	}
}
