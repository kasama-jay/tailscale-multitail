// Package dnsmux serves merged effective-IP MagicDNS records.
package dnsmux

import (
	"context"
	"github.com/jay/tailscale-multitail/internal/inventory"
	"github.com/miekg/dns"
	"net"
	"net/netip"
	"strings"
	"sync"
)

const TTL = 30

type Query func(context.Context, string, string, string) ([]byte, error)
type Server struct {
	mu        sync.RWMutex
	targets   []inventory.Target
	effective map[string]netip.Addr
	reverse   map[netip.Addr]string
	suffix    map[string]string
	query     Query
	udp, tcp  *dns.Server
}

func New(targets []inventory.Target, effective map[string]netip.Addr, q Query) *Server {
	s := &Server{targets: append([]inventory.Target(nil), targets...), effective: effective, reverse: map[netip.Addr]string{}, suffix: map[string]string{}, query: q}
	for _, t := range targets {
		if t.FQDN != "" {
			f := norm(t.FQDN)
			if ip := effective[inventory.Key(t)]; ip.IsValid() {
				s.reverse[ip] = f
			}
			parts := strings.SplitN(f, ".", 2)
			if len(parts) == 2 {
				s.suffix[parts[1]] = t.ProfileID
			}
		}
	}
	return s
}
func norm(n string) string { return strings.TrimSuffix(strings.ToLower(n), ".") + "." }
func (s *Server) Start(addr string) error {
	h := dns.HandlerFunc(s.ServeDNS)
	s.udp = &dns.Server{Addr: addr, Net: "udp", Handler: h}
	s.tcp = &dns.Server{Addr: addr, Net: "tcp", Handler: h}
	go s.udp.ListenAndServe()
	go s.tcp.ListenAndServe()
	return nil
}
func (s *Server) Close() error {
	var e error
	if s.udp != nil {
		e = s.udp.Shutdown()
	}
	if s.tcp != nil {
		if x := s.tcp.Shutdown(); e == nil {
			e = x
		}
	}
	return e
}
func (s *Server) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)
	if len(r.Question) != 1 {
		m.Rcode = dns.RcodeFormatError
		w.WriteMsg(m)
		return
	}
	q := r.Question[0]
	name := norm(q.Name)
	if q.Qtype == dns.TypeAAAA {
		m.Authoritative = true
		w.WriteMsg(m)
		return
	}
	if q.Qtype == dns.TypePTR {
		if s.ptr(m, q, name) {
			w.WriteMsg(m)
			return
		}
		m.Rcode = dns.RcodeNameError
		w.WriteMsg(m)
		return
	}
	if q.Qtype != dns.TypeA {
		m.Rcode = dns.RcodeNotImplemented
		w.WriteMsg(m)
		return
	}
	if s.localA(m, q, name) {
		w.WriteMsg(m)
		return
	}
	if pid := s.profileFor(name); pid != "" && s.query != nil {
		b, e := s.query(context.Background(), pid, q.Name, "A")
		if e == nil {
			up := new(dns.Msg)
			if up.Unpack(b) == nil {
				s.rewrite(up)
				up.Id = r.Id
				w.WriteMsg(up)
				return
			}
		}
	}
	m.Rcode = dns.RcodeNameError
	w.WriteMsg(m)
}
func (s *Server) localA(m *dns.Msg, q dns.Question, name string) bool {
	for _, t := range s.targets {
		match := norm(t.FQDN) == name
		if !match && strings.Count(strings.TrimSuffix(name, "."), ".") == 0 && t.FQDN != "" {
			match = strings.Split(norm(t.FQDN), ".")[0] == strings.TrimSuffix(name, ".")
		}
		if match {
			if ip := s.effective[inventory.Key(t)]; ip.IsValid() {
				m.Authoritative = true
				m.Answer = append(m.Answer, &dns.A{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: TTL}, A: net.IP(ip.AsSlice())})
				return true
			}
		}
	}
	return false
}
func (s *Server) ptr(m *dns.Msg, q dns.Question, name string) bool {
	for ip, f := range s.reverse {
		want, _ := dns.ReverseAddr(ip.String())
		if norm(want) == name {
			m.Authoritative = true
			m.Answer = append(m.Answer, &dns.PTR{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypePTR, Class: dns.ClassINET, Ttl: TTL}, Ptr: f})
			return true
		}
	}
	return false
}
func (s *Server) profileFor(n string) string {
	for suffix, p := range s.suffix {
		if strings.HasSuffix(n, suffix) {
			return p
		}
	}
	return ""
}
func (s *Server) rewrite(m *dns.Msg) {
	for _, rr := range m.Answer {
		if a, ok := rr.(*dns.A); ok {
			ip, ok := netip.AddrFromSlice(a.A)
			if !ok {
				continue
			}
			for _, t := range s.targets {
				if t.CanonicalIP == ip {
					if e := s.effective[inventory.Key(t)]; e.IsValid() {
						a.A = net.IP(e.AsSlice())
						a.Hdr.Ttl = min(a.Hdr.Ttl, TTL)
					}
				}
			}
		}
	}
}
func min(a, b uint32) uint32 {
	if a < b {
		return a
	}
	return b
}
