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
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Binding struct {
	Target    inventory.Target
	Effective netip.Addr
	Self      netip.Addr
	Tun       *packettun.Device
}

type flowState struct {
	seen  time.Time
	proto byte
}

type Stats struct {
	HostPackets        uint64 `json:"host_packets"`
	ProfilePackets     uint64 `json:"profile_packets"`
	Drops              uint64 `json:"drops"`
	FlowLimitDrops     uint64 `json:"flow_limit_drops"`
	FragmentLimitDrops uint64 `json:"fragment_limit_drops"`
	PurgedFlows        uint64 `json:"purged_flows"`
	PurgedFragments    uint64 `json:"purged_fragments"`
	RateLimitedErrors  uint64 `json:"rate_limited_errors"`
	Flows              int    `json:"flows"`
	Fragments          int    `json:"fragments"`
}

type fragmentState struct {
	raw     bool
	binding Binding
	seen    time.Time
}

type Engine struct {
	host               *hosttun.Device
	Debug              bool
	nat                netip.Addr
	byEffective        map[netip.Addr]Binding
	byInbound          map[string]Binding
	raw                map[netip.Addr]Binding
	selfByProfile      map[string]netip.Addr
	flows              map[string]flowState
	flowMu             sync.Mutex
	bindingsMu         sync.RWMutex
	fragments          map[string]fragmentState
	fragmentMu         sync.Mutex
	hostPackets        atomic.Uint64
	profilePackets     atomic.Uint64
	drops              atomic.Uint64
	flowLimitDrops     atomic.Uint64
	fragmentLimitDrops atomic.Uint64
	purgedFlows        atomic.Uint64
	purgedFragments    atomic.Uint64
	rateLimitedErrors  atomic.Uint64
	warningMu          sync.Mutex
	warnings           map[string]time.Time
	profileMu          sync.Mutex
	profileSeen        map[*packettun.Device]bool
	profiles           []runtime.DatapathProfile
	ctx                context.Context
	wg                 sync.WaitGroup
}

func New(h *hosttun.Device, nat netip.Addr, targets []inventory.Target, leases map[string]netip.Addr, profiles []runtime.DatapathProfile) (*Engine, error) {
	self := map[string]runtime.DatapathProfile{}
	e := &Engine{
		host:          h,
		nat:           nat,
		byEffective:   map[netip.Addr]Binding{},
		byInbound:     map[string]Binding{},
		raw:           map[netip.Addr]Binding{},
		selfByProfile: map[string]netip.Addr{},
		flows:         map[string]flowState{},
		fragments:     map[string]fragmentState{},
		profileSeen:   map[*packettun.Device]bool{},
		profiles:      append([]runtime.DatapathProfile(nil), profiles...),
		warnings:      map[string]time.Time{},
	}

	for _, p := range profiles {
		self[p.ID] = p
		e.selfByProfile[p.ID] = p.SelfIPv4
	}

	for _, t := range targets {
		p, ok := self[t.ProfileID]
		ip := leases[inventory.Key(t)]
		if !ok || !ip.IsValid() {
			continue
		}
		b := Binding{t, ip, p.SelfIPv4, p.Tun}
		e.byEffective[ip] = b
		e.byInbound[t.ProfileID+"/"+t.CanonicalIP.String()] = b
		if _, exists := e.raw[t.CanonicalIP]; !exists {
			e.raw[t.CanonicalIP] = b
		}
	}

	return e, nil
}

func (e *Engine) Update(targets []inventory.Target, leases map[string]netip.Addr, profiles []runtime.DatapathProfile) {
	n, _ := New(e.host, e.nat, targets, leases, profiles)

	e.bindingsMu.Lock()
	removed := make(map[string]bool)
	for id := range e.selfByProfile {
		if _, ok := n.selfByProfile[id]; !ok {
			removed[id] = true
		}
	}
	e.byEffective = n.byEffective
	e.byInbound = n.byInbound
	e.raw = n.raw
	e.selfByProfile = n.selfByProfile
	e.bindingsMu.Unlock()

	e.purgeProfiles(removed)
	e.ensureProfileLoops(profiles)
}

func (e *Engine) purgeProfiles(removed map[string]bool) {
	if len(removed) == 0 {
		return
	}

	var flows, fragments uint64
	e.flowMu.Lock()
	for key := range e.flows {
		for id := range removed {
			if strings.HasPrefix(key, id+"/") {
				delete(e.flows, key)
				flows++
				break
			}
		}
	}
	e.flowMu.Unlock()

	e.fragmentMu.Lock()
	for key := range e.fragments {
		for id := range removed {
			if strings.HasPrefix(key, id+"/") {
				delete(e.fragments, key)
				fragments++
				break
			}
		}
	}
	e.fragmentMu.Unlock()

	e.purgedFlows.Add(flows)
	e.purgedFragments.Add(fragments)
	log.Printf("mux: purged state for withdrawn profiles: flows=%d fragments=%d", flows, fragments)
}

func (e *Engine) Run(ctx context.Context) {
	e.ctx = ctx
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.hostLoop(ctx)
	}()
	e.ensureProfileLoops(e.profiles)
}

func (e *Engine) ensureProfileLoops(profiles []runtime.DatapathProfile) {
	e.profileMu.Lock()
	defer e.profileMu.Unlock()
	if e.ctx == nil {
		return
	}

	for _, p := range profiles {
		if e.profileSeen[p.Tun] {
			continue
		}
		e.profileSeen[p.Tun] = true
		e.wg.Add(1)
		go func(p runtime.DatapathProfile) { defer e.wg.Done(); e.profileLoop(e.ctx, p.Tun, p.ID) }(p)
	}
}

func (e *Engine) Wait() {
	e.wg.Wait()
}

func (e *Engine) trace(f string, a ...any) {
	if e.Debug {
		log.Printf("mux: "+f, a...)
	}
}

func (e *Engine) warnf(key, f string, a ...any) {
	e.warningMu.Lock()
	last := e.warnings[key]
	if time.Since(last) < 30*time.Second {
		e.warningMu.Unlock()
		return
	}
	e.warnings[key] = time.Now()
	e.warningMu.Unlock()

	e.rateLimitedErrors.Add(1)
	log.Printf("mux: "+f, a...)
}

func (e *Engine) dropf(key, f string, a ...any) {
	e.drops.Add(1)
	e.warnf(key, f, a...)
}

func (e *Engine) hostLoop(ctx context.Context) {
	buf := make([]byte, 65535)
	for {
		p, err := e.host.ReadPacket(buf)
		if err != nil {
			log.Printf("mux: host TUN read stopped: %v", err)
			return
		}

		e.hostPackets.Add(1)
		src, dst, proto, err := ipv4(p)
		if err != nil || src != e.nat {
			e.dropf("host-invalid", "dropping invalid host packet")
			continue
		}

		e.bindingsMu.RLock()
		b, ok := e.byEffective[dst]
		if !ok {
			b, ok = e.raw[dst]
		}
		e.bindingsMu.RUnlock()
		raw := dst != b.Effective
		if !ok {
			e.dropf("host-unmatched", "dropping host packet with no multitail route")
			continue
		}

		offset, _ := fragmentBits(p)
		if raw && offset == 0 && !e.addFlow(flowKey(b.Target.ProfileID, p, false), proto) {
			e.dropf("flow-capacity", "dropping packet because the raw flow table is full")
			continue
		}

		if err := rewrite(p, b.Self, b.Target.CanonicalIP, proto); err == nil {
			e.trace("host inject %s -> %s via %s", src, dst, b.Target.ProfileID)
			if err := b.Tun.TryInject(p); err != nil {
				e.drops.Add(1)
				e.trace("host inject drop via %s: %v", b.Target.ProfileID, err)
			}
		} else {
			e.dropf("host-rewrite", "dropping packet after host address translation failure: %v", err)
		}
	}
}

func (e *Engine) profileLoop(ctx context.Context, t *packettun.Device, pid string) {
	for {
		p, err := t.Receive()
		if err != nil {
			return
		}

		e.profilePackets.Add(1)
		src, dst, proto, err := ipv4(p)
		e.bindingsMu.RLock()
		self := e.selfByProfile[pid]
		e.bindingsMu.RUnlock()
		if err != nil || dst != self {
			e.trace("profile=%s drop src=%s dst=%s err=%v", pid, src, dst, err)
			continue
		}

		offset, mf := fragmentBits(p)
		if offset > 0 {
			fs, ok := e.getFragment(fragmentKey(pid, p))
			if !ok {
				e.trace("profile=%s drop unassociated fragment", pid)
				continue
			}

			if fs.raw {
				if err := rewrite(p, src, e.nat, proto); err == nil {
					_ = e.host.WritePacket(p)
				}
			} else {
				if err := rewrite(p, fs.binding.Effective, e.nat, proto); err == nil {
					_ = e.host.WritePacket(p)
				}
			}
			continue
		}

		if e.touchFlow(flowKey(pid, p, true), proto) {
			if mf {
				e.putFragment(fragmentKey(pid, p), fragmentState{raw: true, seen: time.Now()})
			}
			if err := rewrite(p, src, e.nat, proto); err == nil {
				e.trace("profile=%s raw return %s", pid, src)
				if err := e.host.WritePacket(p); err != nil {
					e.trace("host write error: %v", err)
				}
			}
			continue
		}

		e.bindingsMu.RLock()
		b, ok := e.byInbound[pid+"/"+src.String()]
		e.bindingsMu.RUnlock()
		if !ok {
			e.trace("profile=%s drop unknown source=%s", pid, src)
			continue
		}

		if mf {
			e.putFragment(fragmentKey(pid, p), fragmentState{binding: b, seen: time.Now()})
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

func flowKey(pid string, p []byte, inbound bool) string {
	src, dst, proto, err := ipv4(p)
	if err != nil {
		return ""
	}

	n := int(p[0]&15) * 4
	remote := dst
	if inbound {
		remote = src
	}

	local, remotePort := uint16(0), uint16(0)
	if proto == 6 || proto == 17 {
		if len(p) < n+4 {
			return ""
		}

		a, b := binary.BigEndian.Uint16(p[n:n+2]), binary.BigEndian.Uint16(p[n+2:n+4])
		if inbound {
			remotePort = a
			local = b
		} else {
			local = a
			remotePort = b
		}
	} else if proto == 1 {
		if len(p) < n+6 {
			return ""
		}
		local = binary.BigEndian.Uint16(p[n+4 : n+6])
	} else {
		return ""
	}

	return fmt.Sprintf("%s/%d/%s/%d/%d", pid, proto, remote, remotePort, local)
}

func flowTimeout(proto byte) time.Duration {
	if proto == 6 {
		return 5 * time.Minute
	}
	if proto == 17 {
		return time.Minute
	}
	return 30 * time.Second
}

func (e *Engine) addFlow(k string, proto byte) bool {
	if k == "" {
		return false
	}

	e.flowMu.Lock()
	defer e.flowMu.Unlock()
	now := time.Now()
	for x, t := range e.flows {
		if now.Sub(t.seen) > flowTimeout(t.proto) {
			delete(e.flows, x)
		}
	}

	if len(e.flows) >= 65536 {
		e.flowLimitDrops.Add(1)
		return false
	}

	e.flows[k] = flowState{now, proto}
	return true
}

func (e *Engine) touchFlow(k string, proto byte) bool {
	if k == "" {
		return false
	}

	e.flowMu.Lock()
	defer e.flowMu.Unlock()
	t, ok := e.flows[k]
	if !ok || time.Since(t.seen) > flowTimeout(t.proto) {
		delete(e.flows, k)
		return false
	}

	e.flows[k] = flowState{time.Now(), proto}
	return true
}

func fragmentBits(p []byte) (uint16, bool) {
	if len(p) < 8 {
		return 0, false
	}
	v := binary.BigEndian.Uint16(p[6:8])
	return v & 0x1fff, v&0x2000 != 0
}

func fragmentKey(pid string, p []byte) string {
	if len(p) < 20 {
		return ""
	}
	return fmt.Sprintf("%s/%d/%x/%x/%d", pid, p[9], p[12:16], p[16:20], binary.BigEndian.Uint16(p[4:6]))
}

func (e *Engine) putFragment(k string, v fragmentState) {
	if k == "" {
		return
	}

	e.fragmentMu.Lock()
	defer e.fragmentMu.Unlock()
	now := time.Now()
	for x, s := range e.fragments {
		if now.Sub(s.seen) > 30*time.Second {
			delete(e.fragments, x)
		}
	}

	if len(e.fragments) >= 8192 {
		e.fragmentLimitDrops.Add(1)
		return
	}

	v.seen = now
	e.fragments[k] = v
}

func (e *Engine) getFragment(k string) (fragmentState, bool) {
	e.fragmentMu.Lock()
	defer e.fragmentMu.Unlock()
	v, ok := e.fragments[k]
	if !ok || time.Since(v.seen) > 30*time.Second {
		delete(e.fragments, k)
		return fragmentState{}, false
	}

	v.seen = time.Now()
	e.fragments[k] = v
	return v, true
}

func (e *Engine) Stats() Stats {
	e.flowMu.Lock()
	flows := len(e.flows)
	e.flowMu.Unlock()

	e.fragmentMu.Lock()
	fragments := len(e.fragments)
	e.fragmentMu.Unlock()

	return Stats{
		HostPackets:        e.hostPackets.Load(),
		ProfilePackets:     e.profilePackets.Load(),
		Drops:              e.drops.Load(),
		FlowLimitDrops:     e.flowLimitDrops.Load(),
		FragmentLimitDrops: e.fragmentLimitDrops.Load(),
		PurgedFlows:        e.purgedFlows.Load(),
		PurgedFragments:    e.purgedFragments.Load(),
		RateLimitedErrors:  e.rateLimitedErrors.Load(),
		Flows:              flows,
		Fragments:          fragments,
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
	return netip.AddrFrom4([4]byte(p[12:16])), netip.AddrFrom4([4]byte(p[16:20])), p[9], nil
}

func rewrite(p []byte, src, dst netip.Addr, proto byte) error {
	n := int(p[0]&15) * 4
	if len(p) < n {
		return fmt.Errorf("short")
	}

	oldSrc := append([]byte(nil), p[12:16]...)
	oldDst := append([]byte(nil), p[16:20]...)
	copy(p[12:16], src.AsSlice())
	copy(p[16:20], dst.AsSlice())
	p[10], p[11] = 0, 0
	binary.BigEndian.PutUint16(p[10:12], sum(p[:n]))
	offset, mf := fragmentBits(p)
	if offset > 0 {
		return nil
	}

	if mf && (proto == 6 || proto == 17) {
		off := n + 16
		if proto == 17 {
			off = n + 6
		}
		if len(p) < off+2 {
			return fmt.Errorf("short fragmented transport")
		}
		old := binary.BigEndian.Uint16(p[off : off+2])
		binary.BigEndian.PutUint16(p[off:off+2], adjustChecksum(old, append(oldSrc, oldDst...), append(src.AsSlice(), dst.AsSlice()...)))
		return nil
	}

	if mf && proto == 1 {
		return nil
	}

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

func adjustChecksum(old uint16, oldWords, newWords []byte) uint16 {
	s := uint32(^old)
	for i := 0; i+1 < len(oldWords); i += 2 {
		s += uint32(^binary.BigEndian.Uint16(oldWords[i:i+2])) + uint32(binary.BigEndian.Uint16(newWords[i:i+2]))
		for s>>16 != 0 {
			s = (s & 0xffff) + (s >> 16)
		}
	}
	return ^uint16(s)
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
