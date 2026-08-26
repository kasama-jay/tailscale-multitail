//go:build linux

// Package hosttun owns the Linux-visible multitail TUN and its narrow routes.
package hosttun

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"

	"github.com/jay/tailscale-multitail/internal/ip"
	"github.com/tailscale/wireguard-go/device"
	"github.com/tailscale/wireguard-go/tun"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const RulePriority = 5260

type Device struct {
	Tun     tun.Device
	Name    string
	link    netlink.Link
	table   int
	routes  []netlink.Route
	rule    netlink.Rule
	pending [][]byte
}

func CheckNativeTailscale() error {
	if _, e := netlink.LinkByName("tailscale0"); e == nil {
		return errors.New("native tailscale0 exists; stop tailscaled before starting tailscale-multitaild")
	}

	ents, e := os.ReadDir("/proc")
	if e != nil {
		return nil
	}

	for _, x := range ents {
		if _, e := strconv.Atoi(x.Name()); e != nil {
			continue
		}

		b, e := os.ReadFile(filepath.Join("/proc", x.Name(), "comm"))
		if e == nil && string(b) == "tailscaled\n" {
			return errors.New("native tailscaled is active; stop it before starting tailscale-multitaild")
		}
	}
	return nil
}

func CheckPoolOverlap(name string, table int, pool netip.Prefix) error {
	links, e := netlink.LinkList()
	if e != nil {
		return e
	}

	for _, l := range links {
		if l.Attrs().Name == name {
			continue
		}
		aa, _ := netlink.AddrList(l, netlink.FAMILY_V4)
		for _, a := range aa {
			p, e := netip.ParsePrefix(a.IPNet.String())
			if e == nil && (pool.Contains(p.Addr()) || p.Contains(pool.Addr())) {
				return fmt.Errorf("effective pool %s overlaps address %s on %s", pool, p, l.Attrs().Name)
			}
		}
	}

	rr, e := netlink.RouteList(nil, netlink.FAMILY_V4)
	if e != nil {
		return e
	}

	for _, r := range rr {
		ones := 0
		if r.Dst != nil {
			ones, _ = r.Dst.Mask.Size()
		}
		if r.Dst == nil || ones == 0 || r.Table == table {
			continue
		}
		p, e := netip.ParsePrefix(r.Dst.String())
		if e == nil && (pool.Contains(p.Addr()) || p.Contains(pool.Addr())) {
			return fmt.Errorf("effective pool %s overlaps route %s table %d", pool, p, r.Table)
		}
	}

	return nil
}

func Create(name string, mtu, table int, pool netip.Prefix) (*Device, error) {
	if e := CheckNativeTailscale(); e != nil {
		return nil, e
	}

	if e := CheckPoolOverlap(name, table, pool); e != nil {
		return nil, e
	}

	if _, e := netlink.LinkByName(name); e == nil {
		return nil, fmt.Errorf("interface %q already exists", name)
	}

	if e := checkPriorities(); e != nil {
		return nil, e
	}

	td, e := tun.CreateTUN(name, mtu)
	if e != nil {
		return nil, e
	}

	actual, e := td.Name()
	if e != nil {
		td.Close()
		return nil, e
	}

	l, e := netlink.LinkByName(actual)
	if e != nil {
		td.Close()
		return nil, e
	}

	d := &Device{
		Tun:   td,
		Name:  actual,
		link:  l,
		table: table,
		rule:  *netlink.NewRule(),
	}
	if e = d.setup(pool); e != nil {
		d.Close()
		return nil, e
	}

	return d, nil
}

func checkPriorities() error {
	r, e := netlink.RuleList(netlink.FAMILY_V4)
	if e != nil {
		return e
	}

	for _, x := range r {
		if x.Priority >= 5260 && x.Priority <= 5269 {
			return fmt.Errorf("ip rule priority %d is already occupied", x.Priority)
		}
	}
	return nil
}

func (d *Device) setup(pool netip.Prefix) error {
	base := pool.Masked().Addr().As4()
	n := ip.IPv4NumFromBytes(base)
	for _, v := range []uint32{n + 1, n + 2} {
		ip := ip.IPv4FromNum(v)
		addr := &netlink.Addr{
			IPNet: &net.IPNet{
				IP:   ip,
				Mask: net.CIDRMask(32, 32),
			},
		}
		if e := netlink.AddrAdd(d.link, addr); e != nil && !errors.Is(e, unix.EEXIST) {
			return e
		}
	}

	if e := netlink.LinkSetUp(d.link); e != nil {
		return e
	}

	d.rule.Priority = RulePriority
	d.rule.Table = d.table
	if e := netlink.RuleAdd(&d.rule); e != nil {
		return e
	}

	return nil
}

func (d *Device) Index() int {
	return d.link.Attrs().Index
}

func (d *Device) ReconcileTargets(ips []netip.Addr) error {
	for i := len(d.routes) - 1; i >= 0; i-- {
		_ = netlink.RouteDel(&d.routes[i])
	}
	d.routes = nil
	return d.AddTargets(ips)
}

func (d *Device) AddTargets(ips []netip.Addr) error {
	seen := map[netip.Addr]bool{}
	for _, ip := range ips {
		if !ip.Is4() || seen[ip] {
			continue
		}

		seen[ip] = true
		dst := netip.PrefixFrom(ip, 32)
		r := netlink.Route{
			LinkIndex: d.link.Attrs().Index,
			Dst:       netipToIPNet(dst),
			Table:     d.table,
			Scope:     netlink.SCOPE_LINK,
		}
		if e := netlink.RouteAdd(&r); e != nil {
			return fmt.Errorf("add route %s: %w", dst, e)
		}

		d.routes = append(d.routes, r)
	}
	return nil
}

func netipToIPNet(p netip.Prefix) *net.IPNet {
	return &net.IPNet{
		IP:   net.IP(p.Addr().AsSlice()),
		Mask: net.CIDRMask(p.Bits(), 32),
	}
}

func (d *Device) ReadPacket(buf []byte) ([]byte, error) {
	if len(d.pending) != 0 {
		p := d.pending[0]
		d.pending = d.pending[1:]
		return p, nil
	}

	if len(buf) < device.MessageTransportHeaderSize+1 {
		return nil, fmt.Errorf("host TUN buffer too short")
	}

	batchSize := d.Tun.BatchSize()
	if batchSize < 1 {
		batchSize = 1
	}

	bufs := make([][]byte, batchSize)
	bufs[0] = buf
	for i := 1; i < batchSize; i++ {
		bufs[i] = make([]byte, len(buf))
	}

	sizes := make([]int, batchSize)
	n, err := d.Tun.Read(bufs, sizes, device.MessageTransportHeaderSize)
	if err != nil {
		return nil, err
	}

	if n < 1 || n > len(bufs) {
		return nil, fmt.Errorf("invalid host TUN batch %d", n)
	}

	for i := 0; i < n; i++ {
		if sizes[i] < 1 || device.MessageTransportHeaderSize+sizes[i] > len(bufs[i]) {
			return nil, fmt.Errorf("invalid host TUN packet size %d", sizes[i])
		}
		headerSize := device.MessageTransportHeaderSize
		d.pending = append(
			d.pending,
			append([]byte(nil), bufs[i][headerSize:headerSize+sizes[i]]...),
		)
	}

	p := d.pending[0]
	d.pending = d.pending[1:]
	return p, nil
}

func (d *Device) WritePacket(p []byte) error {
	buf := make([]byte, device.MessageTransportHeaderSize+len(p))
	copy(buf[device.MessageTransportHeaderSize:], p)
	_, err := d.Tun.Write([][]byte{buf}, device.MessageTransportHeaderSize)
	return err
}

func (d *Device) Close() error {
	for i := len(d.routes) - 1; i >= 0; i-- {
		_ = netlink.RouteDel(&d.routes[i])
	}

	if d.rule.Priority != 0 {
		_ = netlink.RuleDel(&d.rule)
	}

	if d.Tun != nil {
		return d.Tun.Close()
	}

	return nil
}
