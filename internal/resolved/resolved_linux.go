//go:build linux

package resolved

import (
	"context"
	"fmt"
	"github.com/godbus/dbus/v5"
	"net/netip"
	"strings"
)

type Client struct {
	conn    *dbus.Conn
	ifindex int32
}
type dnsAddr struct {
	Family  int32
	Address []byte
}
type domain struct {
	Name      string
	RouteOnly bool
}

func Configure(ctx context.Context, ifindex int, dnsIP netip.Addr, suffixes, routeOnly []string) (*Client, error) {
	if !dnsIP.Is4() {
		return nil, fmt.Errorf("resolved DNS address must be IPv4")
	}
	c, e := dbus.ConnectSystemBus()
	if e != nil {
		return nil, e
	}
	cl := &Client{c, int32(ifindex)}
	o := c.Object("org.freedesktop.resolve1", dbus.ObjectPath("/org/freedesktop/resolve1"))
	if e = o.CallWithContext(ctx, "org.freedesktop.resolve1.Manager.SetLinkDNS", 0, int32(ifindex), []dnsAddr{{2, dnsIP.AsSlice()}}).Err; e != nil {
		c.Close()
		return nil, e
	}
	// The local DNS mux synthesizes effective A/PTR records and forwards
	// profile DNS replies; it does not provide DNSSEC signatures. Force this
	// link's validation policy off rather than inheriting a global "yes" policy
	// that would reject every unsigned MagicDNS answer.
	if e = o.CallWithContext(ctx, "org.freedesktop.resolve1.Manager.SetLinkDNSSEC", 0, int32(ifindex), "no").Err; e != nil {
		cl.Revert(context.Background())
		return nil, e
	}
	seen := map[string]bool{}
	ds := make([]domain, 0, len(suffixes)+len(routeOnly))
	for _, s := range suffixes {
		s = strings.Trim(strings.ToLower(s), ".")
		if s != "" && !seen[s] {
			seen[s] = true
			ds = append(ds, domain{s, false})
		}
	}
	for _, s := range routeOnly {
		s = strings.Trim(strings.ToLower(s), ".")
		if s != "" && !seen[s] {
			seen[s] = true
			ds = append(ds, domain{s, true})
		}
	}
	if e = o.CallWithContext(ctx, "org.freedesktop.resolve1.Manager.SetLinkDomains", 0, int32(ifindex), ds).Err; e != nil {
		cl.Revert(context.Background())
		return nil, e
	}
	if e = o.CallWithContext(ctx, "org.freedesktop.resolve1.Manager.SetLinkDefaultRoute", 0, int32(ifindex), false).Err; e != nil {
		cl.Revert(context.Background())
		return nil, e
	}
	return cl, nil
}
func (c *Client) Revert(ctx context.Context) error {
	if c == nil || c.conn == nil {
		return nil
	}
	e := c.conn.Object("org.freedesktop.resolve1", dbus.ObjectPath("/org/freedesktop/resolve1")).CallWithContext(ctx, "org.freedesktop.resolve1.Manager.RevertLink", 0, c.ifindex).Err
	c.conn.Close()
	c.conn = nil
	return e
}
