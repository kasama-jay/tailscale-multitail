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

	"github.com/tailscale/wireguard-go/device"
	"github.com/tailscale/wireguard-go/tun"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const RulePriority = 5260

type Device struct {
	Tun    tun.Device
	Name   string
	link   netlink.Link
	table  int
	routes []netlink.Route
	rule   netlink.Rule
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
func Create(name string, mtu, table int, pool netip.Prefix) (*Device, error) {
	if e := CheckNativeTailscale(); e != nil {
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
	d := &Device{Tun: td, Name: actual, link: l, table: table, rule: *netlink.NewRule()}
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
	n := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
	for _, v := range []uint32{n + 1, n + 2} {
		ip := net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
		if e := netlink.AddrAdd(d.link, &netlink.Addr{IPNet: &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)}}); e != nil && !errors.Is(e, unix.EEXIST) {
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
func (d *Device) AddTargets(ips []netip.Addr) error {
	seen := map[netip.Addr]bool{}
	for _, ip := range ips {
		if !ip.Is4() || seen[ip] {
			continue
		}
		seen[ip] = true
		dst := netip.PrefixFrom(ip, 32)
		r := netlink.Route{LinkIndex: d.link.Attrs().Index, Dst: netipToIPNet(dst), Table: d.table, Scope: netlink.SCOPE_LINK}
		if e := netlink.RouteAdd(&r); e != nil {
			return fmt.Errorf("add route %s: %w", dst, e)
		}
		d.routes = append(d.routes, r)
	}
	return nil
}
func netipToIPNet(p netip.Prefix) *net.IPNet {
	return &net.IPNet{IP: net.IP(p.Addr().AsSlice()), Mask: net.CIDRMask(p.Bits(), 32)}
}
func (d *Device) ReadPacket(buf []byte) ([]byte, error) {
	if len(buf) < device.MessageTransportHeaderSize+1 {
		return nil, fmt.Errorf("host TUN buffer too short")
	}
	sizes := make([]int, 1)
	n, err := d.Tun.Read([][]byte{buf}, sizes, device.MessageTransportHeaderSize)
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, fmt.Errorf("unexpected host TUN batch %d", n)
	}
	return append([]byte(nil), buf[device.MessageTransportHeaderSize:device.MessageTransportHeaderSize+sizes[0]]...), nil
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
