// Package mux translates IPv4 effective-target traffic between the host TUN
// and isolated embedded tsnet profile TUNs.
package mux

import (
	"context"
	"encoding/binary"
	"fmt"
	"github.com/jay/tailscale-multitail/internal/hosttun"
	"github.com/jay/tailscale-multitail/internal/inventory"
	"github.com/jay/tailscale-multitail/internal/packettun"
	"github.com/jay/tailscale-multitail/internal/runtime"
	"log"
	"net/netip"
	"sync"
)

type Binding struct {
	Target    inventory.Target
	Effective netip.Addr
	Self      netip.Addr
	Tun       *packettun.Device
}
type Engine struct {
	host        *hosttun.Device
	Debug       bool
	nat         netip.Addr
	byEffective map[netip.Addr]Binding
	byInbound   map[string]Binding
	wg          sync.WaitGroup
}

func New(h *hosttun.Device, nat netip.Addr, targets []inventory.Target, leases map[string]netip.Addr, profiles []runtime.DatapathProfile) (*Engine, error) {
	self := map[string]runtime.DatapathProfile{}
	for _, p := range profiles {
		self[p.ID] = p
	}
	e := &Engine{host: h, nat: nat, byEffective: map[netip.Addr]Binding{}, byInbound: map[string]Binding{}}
	for _, t := range targets {
		p, ok := self[t.ProfileID]
		ip := leases[inventory.Key(t)]
		if !ok || !ip.IsValid() {
			continue
		}
		b := Binding{t, ip, p.SelfIPv4, p.Tun}
		e.byEffective[ip] = b
		e.byInbound[t.ProfileID+"/"+t.CanonicalIP.String()] = b
	}
	return e, nil
}
func (e *Engine) Run(ctx context.Context) {
	e.wg.Add(1)
	go func() { defer e.wg.Done(); e.hostLoop(ctx) }()
	seen := map[*packettun.Device]bool{}
	for _, b := range e.byEffective {
		if seen[b.Tun] {
			continue
		}
		seen[b.Tun] = true
		e.wg.Add(1)
		go func(b Binding) { defer e.wg.Done(); e.profileLoop(ctx, b.Tun, b.Target.ProfileID, b.Self) }(b)
	}
}
func (e *Engine) Wait() { e.wg.Wait() }
func (e *Engine) trace(f string, a ...any) {
	if e.Debug {
		log.Printf("mux: "+f, a...)
	}
}
func (e *Engine) hostLoop(ctx context.Context) {
	buf := make([]byte, 65535)
	for {
		p, err := e.host.ReadPacket(buf)
		if err != nil {
			return
		}
		src, dst, proto, err := ipv4(p)
		if err != nil || src != e.nat {
			e.trace("host drop src=%s dst=%s err=%v", src, dst, err)
			continue
		}
		b, ok := e.byEffective[dst]
		if !ok {
			e.trace("host drop unmatched dst=%s", dst)
			continue
		}
		if err := rewrite(p, b.Self, b.Target.CanonicalIP, proto); err == nil {
			e.trace("host inject %s -> %s via %s", src, dst, b.Target.ProfileID)
			_ = b.Tun.Inject(p)
		} else {
			e.trace("host rewrite drop: %v", err)
		}
	}
}
func (e *Engine) profileLoop(ctx context.Context, t *packettun.Device, pid string, self netip.Addr) {
	for {
		p, err := t.Receive()
		if err != nil {
			return
		}
		src, dst, proto, err := ipv4(p)
		if err != nil || dst != self {
			e.trace("profile=%s drop src=%s dst=%s err=%v", pid, src, dst, err)
			continue
		}
		b, ok := e.byInbound[pid+"/"+src.String()]
		if !ok {
			e.trace("profile=%s drop unknown source=%s", pid, src)
			continue
		}
		if err := rewrite(p, b.Effective, e.nat, proto); err == nil {
			e.trace("profile=%s return %s -> %s", pid, src, dst)
			if err := e.host.WritePacket(p); err != nil {
				e.trace("host write error: %v", err)
			}
		} else {
			e.trace("profile rewrite drop: %v", err)
		}
	}
}
func ipv4(p []byte) (netip.Addr, netip.Addr, byte, error) {
	if len(p) < 20 || p[0]>>4 != 4 {
		return netip.Addr{}, netip.Addr{}, 0, fmt.Errorf("invalid ipv4")
	}
	n := int(p[0]&15) * 4
	if n < 20 || len(p) < n {
		return netip.Addr{}, netip.Addr{}, 0, fmt.Errorf("invalid header")
	}
	if binary.BigEndian.Uint16(p[6:8])&0x3fff != 0 {
		return netip.Addr{}, netip.Addr{}, 0, fmt.Errorf("fragmented packet unsupported")
	}
	return netip.AddrFrom4([4]byte(p[12:16])), netip.AddrFrom4([4]byte(p[16:20])), p[9], nil
}
func rewrite(p []byte, src, dst netip.Addr, proto byte) error {
	n := int(p[0]&15) * 4
	if len(p) < n {
		return fmt.Errorf("short")
	}
	copy(p[12:16], src.AsSlice())
	copy(p[16:20], dst.AsSlice())
	p[10], p[11] = 0, 0
	binary.BigEndian.PutUint16(p[10:12], sum(p[:n]))
	if proto == 1 {
		if len(p) < n+4 {
			return fmt.Errorf("short icmp")
		}
		p[n+2], p[n+3] = 0, 0
		binary.BigEndian.PutUint16(p[n+2:n+4], sum(p[n:]))
		return nil
	}
	if proto == 6 || proto == 17 {
		if len(p) < n+8 {
			return fmt.Errorf("short transport")
		}
		off := n + 16
		if proto == 17 {
			off = n + 6
		}
		p[off], p[off+1] = 0, 0
		ph := make([]byte, 12+len(p[n:]))
		copy(ph, src.AsSlice())
		copy(ph[4:], dst.AsSlice())
		ph[9] = proto
		binary.BigEndian.PutUint16(ph[10:12], uint16(len(p[n:])))
		copy(ph[12:], p[n:])
		c := sum(ph)
		if proto == 17 && c == 0 {
			c = 0xffff
		}
		binary.BigEndian.PutUint16(p[off:off+2], c)
	}
	return nil
}
func sum(p []byte) uint16 {
	var s uint32
	for len(p) > 1 {
		s += uint32(binary.BigEndian.Uint16(p))
		p = p[2:]
	}
	if len(p) == 1 {
		s += uint32(p[0]) << 8
	}
	for s>>16 != 0 {
		s = (s & 0xffff) + (s >> 16)
	}
	return ^uint16(s)
}
