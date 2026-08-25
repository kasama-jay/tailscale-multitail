package dnsmux

import (
	"github.com/jay/tailscale-multitail/internal/inventory"
	"github.com/miekg/dns"
	"net"
	"net/netip"
	"testing"
)

type rw struct{ m *dns.Msg }

func (r *rw) LocalAddr() net.Addr       { return nil }
func (r *rw) RemoteAddr() net.Addr      { return nil }
func (r *rw) WriteMsg(m *dns.Msg) error { r.m = m; return nil }
func (r *rw) Write([]byte) (int, error) { return 0, nil }
func (r *rw) Close() error              { return nil }
func (r *rw) TsigStatus() error         { return nil }
func (r *rw) TsigTimersOnly(bool)       {}
func (r *rw) Hijack()                   {}
func TestEffectiveAAndPTR(t *testing.T) {
	target := inventory.Target{ProfileID: "p", Kind: inventory.Node, ID: "n", FQDN: "db.example.ts.net.", CanonicalIP: netip.MustParseAddr("100.1.2.3")}
	s := New([]inventory.Target{target}, map[string]netip.Addr{inventory.Key(target): netip.MustParseAddr("10.192.4.5")}, nil)
	q := new(dns.Msg)
	q.SetQuestion("db.example.ts.net.", dns.TypeA)
	w := &rw{}
	s.ServeDNS(w, q)
	if len(w.m.Answer) != 1 || w.m.Answer[0].(*dns.A).A.String() != "10.192.4.5" {
		t.Fatalf("%v", w.m.Answer)
	}
	ptr, _ := dns.ReverseAddr("10.192.4.5")
	q.SetQuestion(ptr, dns.TypePTR)
	w = &rw{}
	s.ServeDNS(w, q)
	if len(w.m.Answer) != 1 || w.m.Answer[0].(*dns.PTR).Ptr != "db.example.ts.net." {
		t.Fatalf("%v", w.m.Answer)
	}
}
