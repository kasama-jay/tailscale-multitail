// Package allocator provides deterministic IPv4 effective-address leases.
package allocator

import (
	"crypto/sha256"
	"fmt"
	"net/netip"
	"sort"
)

type Lease struct {
	Key string
	IP  netip.Addr
}
type Allocator struct {
	prefix netip.Prefix
	leases map[string]netip.Addr
	used   map[netip.Addr]string
}

func New(cidr string) (*Allocator, error) {
	p, e := netip.ParsePrefix(cidr)
	if e != nil || !p.Addr().Is4() || p.Bits() > 30 {
		return nil, fmt.Errorf("invalid effective IPv4 CIDR %q", cidr)
	}
	return &Allocator{p, map[string]netip.Addr{}, map[netip.Addr]string{}}, nil
}
func (a *Allocator) Reserve(key string, ip netip.Addr) error {
	if !ip.Is4() || !a.prefix.Contains(ip) || ip == a.prefix.Masked().Addr().Next() || ip == a.prefix.Masked().Addr().Next().Next() {
		return fmt.Errorf("invalid reserved effective IP %s", ip)
	}
	if old, ok := a.used[ip]; ok && old != key {
		return fmt.Errorf("effective IP %s is already leased", ip)
	}
	a.used[ip] = key
	a.leases[key] = ip
	return nil
}
func (a *Allocator) Allocate(keys []string) ([]Lease, error) {
	keys = append([]string(nil), keys...)
	sort.Strings(keys)
	out := make([]Lease, 0, len(keys))
	for _, k := range keys {
		ip, e := a.lease(k)
		if e != nil {
			return nil, e
		}
		out = append(out, Lease{k, ip})
	}
	return out, nil
}
func (a *Allocator) lease(k string) (netip.Addr, error) {
	if ip := a.leases[k]; ip.IsValid() {
		return ip, nil
	}
	base := a.prefix.Masked().Addr().As4()
	n := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
	total := uint32(1) << (32 - a.prefix.Bits())
	if total <= 3 {
		return netip.Addr{}, fmt.Errorf("pool exhausted")
	}
	h := sha256.Sum256([]byte(k))
	slot := (uint32(h[0])<<24|uint32(h[1])<<16|uint32(h[2])<<8|uint32(h[3]))%(total-3) + 3
	for i := uint32(0); i < total-3; i++ {
		v := n + 3 + (slot-3+i)%(total-3)
		ip := netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
		if _, ok := a.used[ip]; !ok {
			a.used[ip] = k
			a.leases[k] = ip
			return ip, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("pool exhausted")
}
