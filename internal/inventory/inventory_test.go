package inventory

import (
	"net/netip"
	"testing"
)

func TestOrderedRawSelectionAndCollision(t *testing.T) {
	ip := netip.MustParseAddr("100.1.2.3")
	s := Build([]Profile{{ID: "later", Name: "later", Order: 1, Targets: []Target{{Kind: Node, ID: "b", CanonicalIP: ip}}}, {ID: "first", Name: "first", Order: 0, Targets: []Target{{Kind: Node, ID: "a", CanonicalIP: ip}}}})
	got, ok := s.ResolveRaw(ip)
	if !ok || got.ProfileID != "first" {
		t.Fatalf("got %#v", got)
	}
	if len(s.Collisions()[ip]) != 2 {
		t.Fatal("missing collision")
	}
}
