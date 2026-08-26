// tailscale-multitaild is the v1 multitail supervisor daemon.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/netip"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jay/tailscale-multitail/internal/config"
	"github.com/jay/tailscale-multitail/internal/control"
	"github.com/jay/tailscale-multitail/internal/dnsmux"
	"github.com/jay/tailscale-multitail/internal/hosttun"
	"github.com/jay/tailscale-multitail/internal/mux"
	resolvedclient "github.com/jay/tailscale-multitail/internal/resolved"
	"github.com/jay/tailscale-multitail/internal/runtime"
)

var version = "devel"

func main() { os.Exit(run()) }
func reverseDomains(c config.Config) []string {
	out := []string{"100.in-addr.arpa"}
	p, e := netip.ParsePrefix(c.EffectiveIPv4CIDR)
	if e != nil || !p.Addr().Is4() {
		return out
	}
	a := p.Masked().Addr().As4()
	n := p.Bits() / 8
	if n > 0 {
		parts := make([]string, n)
		for i := 0; i < n; i++ {
			parts[i] = strconv.Itoa(int(a[n-1-i]))
		}
		out = append(out, strings.Join(parts, ".")+".in-addr.arpa")
	}
	return out
}
func fail(v any) int { log.Print(v); return 1 }
func run() int {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(version)
		return 0
	}
	if len(os.Args) < 2 || os.Args[1] != "run" {
		fmt.Fprintln(os.Stderr, "usage: tailscale-multitaild run [--config PATH] [--state-root PATH] [--validate-config] [--once]")
		return 2
	}
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cp := fs.String("config", config.DefaultPath, "config path")
	root := fs.String("state-root", config.DefaultStateRoot, "state root (test override)")
	valid := fs.Bool("validate-config", false, "validate and exit")
	once := fs.Bool("once", false, "start profiles, print status JSON, and exit")
	dnsListen := fs.String("dns-listen", "", "merged DNS listen address (test override; e.g. 127.0.0.1:1053)")
	hostTUN := fs.Bool("host-tun", false, "create the Linux host TUN and target-specific routes")
	debugPackets := fs.Bool("debug-packets", false, "temporarily log packet-mux decisions")
	socketPath := fs.String("socket", "", "local privileged control socket path")
	useResolved := fs.Bool("resolved", false, "configure systemd-resolved for multitail DNS (requires --host-tun)")
	fs.Parse(os.Args[2:])
	c, e := config.Load(*cp)
	if e != nil {
		return fail(e)
	}
	if *valid {
		fmt.Println("valid")
		return 0
	}
	if *root == config.DefaultStateRoot && filepath.IsAbs(*cp) == false {
		return fail("relative config paths require an explicit --state-root for test safety")
	}
	s, e := runtime.New(c, *root)
	if e != nil {
		return fail(e)
	}
	if e = s.Start(context.Background()); e != nil {
		return fail(e)
	}
	defer s.Close()
	if e = s.ValidateDNSSuffixes(); e != nil {
		return fail(e)
	}
	inv := s.Inventory()
	leases, e := s.EffectiveLeases()
	var h *hosttun.Device
	var m *mux.Engine
	var dnsServer *dnsmux.Server
	var resolved *resolvedclient.Client
	if e != nil {
		return fail(e)
	}
	if *hostTUN {
		pool, _ := netip.ParsePrefix(c.EffectiveIPv4CIDR)
		h, e = hosttun.Create(c.Interface, c.MTU, c.RoutingTable, pool)
		if e != nil {
			return fail(e)
		}
		routes := make([]netip.Addr, 0, len(inv.Targets)+len(leases))
		for _, t := range inv.Targets {
			routes = append(routes, t.CanonicalIP)
		}
		for _, ip := range leases {
			routes = append(routes, ip)
		}
		if e := h.AddTargets(routes); e != nil {
			h.Close()
			return fail(e)
		}
		defer h.Close()
		m, e = mux.New(h, pool.Masked().Addr().Next(), inv.Targets, leases, s.DatapathProfiles())
		if e != nil {
			return fail(e)
		}
		m.Debug = *debugPackets
		m.Run(context.Background())
	}
	if *useResolved {
		if h == nil {
			return fail("--resolved requires --host-tun")
		}
		if *dnsListen == "" {
			pool, _ := netip.ParsePrefix(c.EffectiveIPv4CIDR)
			*dnsListen = pool.Masked().Addr().Next().Next().String() + ":53"
		}
	}
	if *dnsListen != "" {
		dnsServer = dnsmux.New(inv.Targets, leases, s.QueryDNS)
		if e := dnsServer.Start(*dnsListen); e != nil {
			return fail(e)
		}
		defer dnsServer.Close()
	}
	if *useResolved {
		x, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		resolved, e = resolvedclient.Configure(x, h.Index(), netip.MustParseAddr(strings.Split(*dnsListen, ":")[0]), s.DNSSuffixes(), reverseDomains(c))
		cancel()
		if e != nil {
			return fail(e)
		}
		defer func() {
			if resolved != nil {
				_ = resolved.Revert(context.Background())
			}
		}()
	}
	if *once {
		json.NewEncoder(os.Stdout).Encode(struct {
			Profiles   any `json:"profiles"`
			Targets    any `json:"targets"`
			Leases     any `json:"effective_leases"`
			Collisions any `json:"canonical_collisions"`
		}{s.Status(), inv.Targets, leases, inv.Collisions()})
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	shutdown := make(chan struct{})
	var shutdownOnce sync.Once
	var configMu sync.Mutex
	if *socketPath != "" {
		gid := uint32(0)
		if g, e := user.LookupGroup("tsmultitail"); e == nil {
			if n, e := strconv.ParseUint(g.Gid, 10, 32); e == nil {
				gid = uint32(n)
			}
		}
		cs, e := control.Listen(*socketPath, gid, func(req control.Request) control.Response {
			out := control.Response{}
			switch req.Op {
			case "status":
				i := s.Inventory()
				l, _ := s.EffectiveLeases()
				var dp any
				if m != nil {
					dp = m.Stats()
				}
				out.OK = true
				out.Result = struct {
					Profiles any `json:"profiles"`
					Targets  any `json:"targets"`
					Leases   any `json:"effective_leases"`
					Datapath any `json:"datapath,omitempty"`
				}{s.Status(), i.Targets, l, dp}
			case "restart":
				out.OK = true
				out.Restart = true
				go shutdownOnce.Do(func() { close(shutdown) })
			case "login":
				// Profile additions are written to the authoritative YAML before
				// login. Start any newly configured profiles so add+login works
				// without an otherwise unnecessary daemon restart.
				nc, e := config.Load(*cp)
				if e == nil {
					e = s.StartConfiguredProfiles(context.Background(), nc)
				}
				if e != nil {
					out.Error = e.Error()
					break
				}
				x, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				u, e := s.Login(x, req.Profile, req.AuthKey)
				if e != nil {
					out.Error = e.Error()
				} else {
					out.OK = true
					out.Result = map[string]string{"auth_url": u}
				}
			case "logout":
				x, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if e := s.Logout(x, req.Profile); e != nil {
					out.Error = e.Error()
				} else {
					out.OK = true
				}
			case "config_write":
				configMu.Lock()
				defer configMu.Unlock()
				nc, e := config.Parse([]byte(req.ConfigYAML))
				if e == nil {
					e = config.WriteAtomic(*cp, nc)
				}
				if e != nil {
					out.Error = e.Error()
				} else {
					out.OK = true
				}
			default:
				out.Error = "unknown operation"
			}
			return out
		})
		if e != nil {
			return fail(e)
		}
		defer cs.Close()
	}
	for {
		select {
		case <-ctx.Done():
			return 0
		case <-shutdown:
			return 75
		case <-s.Changes():
			if e = s.ValidateDNSSuffixes(); e != nil {
				log.Printf("fatal DNS plan conflict: %v", e)
				return 1
			}
			inv = s.Inventory()
			leases, e = s.EffectiveLeases()
			if e != nil {
				log.Printf("reconcile leases: %v", e)
				continue
			}
			routes := make([]netip.Addr, 0, len(inv.Targets)+len(leases))
			for _, t := range inv.Targets {
				routes = append(routes, t.CanonicalIP)
			}
			for _, ip := range leases {
				routes = append(routes, ip)
			}
			if h != nil {
				if e := h.ReconcileTargets(routes); e != nil {
					log.Printf("reconcile routes: %v", e)
				}
			}
			if m != nil {
				m.Update(inv.Targets, leases, s.DatapathProfiles())
			}
			if dnsServer != nil {
				dnsServer.Update(inv.Targets, leases)
			}
			if *useResolved {
				if resolved != nil {
					_ = resolved.Revert(context.Background())
				}
				x, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				resolved, e = resolvedclient.Configure(x, h.Index(), netip.MustParseAddr(strings.Split(*dnsListen, ":")[0]), s.DNSSuffixes(), reverseDomains(c))
				cancel()
				if e != nil {
					log.Printf("reconcile systemd-resolved: %v", e)
				}
			}
		}
	}
}
