package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"

	"github.com/jay/tailscale-multitail/internal/config"
	"github.com/jay/tailscale-multitail/internal/control"
	"github.com/vishvananda/netlink"
)

var version = "devel"

func die(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...); os.Exit(2) }
func configPath(args *[]string) string {
	p := config.DefaultPath
	for i := 0; i < len(*args); i++ {
		if (*args)[i] == "--config" {
			if i+1 >= len(*args) {
				die("--config needs a path")
			}
			p = (*args)[i+1]
			*args = append((*args)[:i], (*args)[i+2:]...)
			break
		}
	}
	return p
}
func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(version)
		return
	}
	a := os.Args[1:]
	p := configPath(&a)
	socket := "/run/tailscale-multitail/control.sock"
	for i := 0; i < len(a); i++ {
		if a[i] == "--socket" {
			if i+1 >= len(a) {
				die("--socket needs a path")
			}
			socket = a[i+1]
			a = append(a[:i], a[i+2:]...)
			break
		}
	}
	if len(a) == 0 {
		die("usage: tsmultitail [--config PATH] {validate|example-config|config|profiles}")
	}
	switch a[0] {
	case "validate":
		c, e := config.Load(p)
		if e != nil {
			die("%v", e)
		}
		fmt.Printf("valid: %s (%d profiles)\n", p, len(c.Profiles))
	case "example-config":
		b, _ := config.Default().Marshal()
		os.Stdout.Write(b)
	case "config":
		configCmd(socket, p, a[1:])
	case "profiles":
		if len(a) > 1 && (a[1] == "login" || a[1] == "logout") {
			profileLive(socket, a[1:])
			return
		}
		profilesCmd(socket, p, a[1:])
	case "doctor":
		doctor(p)
	case "status":
		r, e := control.Client(socket, "status")
		if e != nil {
			die("status: %v", e)
		}
		json.NewEncoder(os.Stdout).Encode(r.Result)
	case "daemon":
		if len(a) != 2 || a[1] != "restart" {
			die("usage: daemon restart")
		}
		if _, e := control.Client(socket, "restart"); e != nil {
			die("restart: %v", e)
		}
	default:
		die("unknown command %q", a[0])
	}
}
func writeConfig(socket, path string, c config.Config) error {
	if _, e := os.Stat(socket); e == nil {
		b, e := c.Marshal()
		if e != nil {
			return e
		}
		_, e = control.ClientRequest(socket, control.Request{Op: "config_write", ConfigYAML: string(b)})
		return e
	}
	return config.WriteAtomic(path, c)
}
func configCmd(socket, p string, a []string) {
	if len(a) == 0 {
		die("usage: config {init|show|set}")
	}
	switch a[0] {
	case "init":
		if _, e := os.Stat(p); e == nil {
			die("%s already exists (use --force not yet supported)", p)
		}
		if e := writeConfig(socket, p, config.Default()); e != nil {
			die("init: %v", e)
		}
		fmt.Println(p)
	case "show":
		b, e := os.ReadFile(p)
		if e != nil {
			die("%v", e)
		}
		os.Stdout.Write(b)
	case "set":
		if len(a) != 3 {
			die("usage: config set {interface|routing-table|mtu|effective-ipv4-cidr} VALUE")
		}
		c, e := config.Load(p)
		if e != nil {
			die("%v", e)
		}
		switch a[1] {
		case "interface":
			c.Interface = a[2]
		case "routing-table":
			fmt.Sscan(a[2], &c.RoutingTable)
		case "mtu":
			fmt.Sscan(a[2], &c.MTU)
		case "effective-ipv4-cidr":
			c.EffectiveIPv4CIDR = a[2]
		default:
			die("unknown config key %q", a[1])
		}
		if e = writeConfig(socket, p, c); e != nil {
			die("%v", e)
		}
	default:
		die("unknown config command %q", a[0])
	}
}
func profilesCmd(socket, p string, a []string) {
	if len(a) == 0 {
		die("usage: profiles {list|add}")
	}
	c, e := config.Load(p)
	if e != nil {
		die("%v", e)
	}
	switch a[0] {
	case "list":
		for _, x := range c.Profiles {
			fmt.Printf("%s\t%s\t%s\n", x.Name, x.ID, x.Hostname)
		}
	case "remove":
		if len(a) != 2 {
			die("usage: profiles remove NAME")
		}
		i := profileIndex(c, a[1])
		if i < 0 {
			die("profile not found")
		}
		c.Profiles = append(c.Profiles[:i], c.Profiles[i+1:]...)
		if e := writeConfig(socket, p, c); e != nil {
			die("%v", e)
		}
	case "move":
		if len(a) != 4 {
			die("usage: profiles move NAME {--before|--after|--position} VALUE")
		}
		i := profileIndex(c, a[1])
		if i < 0 {
			die("profile not found")
		}
		x := c.Profiles[i]
		c.Profiles = append(c.Profiles[:i], c.Profiles[i+1:]...)
		pos := 0
		switch a[2] {
		case "--position":
			if _, e := fmt.Sscan(a[3], &pos); e != nil {
				die("bad position")
			}
		case "--before", "--after":
			pos = profileIndex(c, a[3])
			if pos < 0 {
				die("reference profile not found")
			}
			if a[2] == "--after" {
				pos++
			}
		default:
			die("unknown move option")
		}
		if pos < 0 || pos > len(c.Profiles) {
			die("position out of range")
		}
		c.Profiles = append(c.Profiles, nilProfile())
		copy(c.Profiles[pos+1:], c.Profiles[pos:])
		c.Profiles[pos] = x
		if e := writeConfig(socket, p, c); e != nil {
			die("%v", e)
		}
	case "set":
		if len(a) != 4 || a[2] != "--control-url" {
			die("usage: profiles set NAME --control-url URL")
		}
		i := profileIndex(c, a[1])
		if i < 0 {
			die("profile not found")
		}
		c.Profiles[i].ControlURL = a[3]
		if e := writeConfig(socket, p, c); e != nil {
			die("%v", e)
		}
	case "add":
		if len(a) < 3 {
			die("usage: profiles add NAME --hostname HOSTNAME [--control-url URL]")
		}
		x := config.Profile{Name: a[1]}
		for i := 2; i < len(a); i += 2 {
			if i+1 >= len(a) {
				die("missing value for %s", a[i])
			}
			switch a[i] {
			case "--hostname":
				x.Hostname = a[i+1]
			case "--control-url":
				x.ControlURL = a[i+1]
			case "--advertise-tag":
				x.AdvertiseTags = append(x.AdvertiseTags, a[i+1])
			default:
				die("unknown flag %s", a[i])
			}
		}
		x.ID = newID()
		c.Profiles = append(c.Profiles, x)
		if e := writeConfig(socket, p, c); e != nil {
			die("%v", e)
		}
		fmt.Println(x.ID)
	default:
		die("unknown profiles command %q", a[0])
	}
}
func nilProfile() config.Profile { return config.Profile{} }
func profileIndex(c config.Config, name string) int {
	for i, p := range c.Profiles {
		if strings.EqualFold(p.Name, name) {
			return i
		}
	}
	return -1
}
func profileLive(socket string, a []string) {
	if len(a) < 2 {
		die("usage: profiles {login|logout} NAME")
	}
	switch a[0] {
	case "login":
		key := ""
		if len(a) == 3 && a[2] == "--auth-key-stdin" {
			b, e := io.ReadAll(io.LimitReader(os.Stdin, 16<<10))
			if e != nil {
				die("read auth key: %v", e)
			}
			key = strings.TrimSpace(string(b))
		} else if len(a) != 2 {
			die("login accepts only --auth-key-stdin")
		}
		r, e := control.ClientRequest(socket, control.Request{Op: "login", Profile: a[1], AuthKey: key})
		key = ""
		if e != nil {
			die("login: %v", e)
		}
		if m, ok := r.Result.(map[string]any); ok {
			if u, _ := m["auth_url"].(string); u != "" {
				fmt.Println(u)
			}
		}
	case "logout":
		if len(a) != 3 || a[2] != "--yes" {
			die("logout requires --yes")
		}
		if _, e := control.ClientRequest(socket, control.Request{Op: "logout", Profile: a[1]}); e != nil {
			die("logout: %v", e)
		}
	}
}
func doctor(path string) {
	c, err := config.Load(path)
	if err != nil {
		die("config: %v", err)
	}
	fmt.Println("ok: strict configuration")
	pool, _ := netip.ParsePrefix(c.EffectiveIPv4CIDR)
	overlap := false
	ifaces, _ := net.Interfaces()
	for _, i := range ifaces {
		if i.Name == c.Interface {
			continue
		}
		aa, _ := i.Addrs()
		for _, a := range aa {
			p, e := netip.ParsePrefix(a.String())
			if e == nil && p.Addr().Is4() && (pool.Contains(p.Addr()) || p.Contains(pool.Addr())) {
				fmt.Printf("warn: effective pool overlaps host address %s on %s\n", p, i.Name)
				overlap = true
			}
		}
	}
	rr, _ := netlink.RouteList(nil, netlink.FAMILY_V4)
	for _, r := range rr {
		ones := 0
		if r.Dst != nil {
			ones, _ = r.Dst.Mask.Size()
		}
		if r.Dst == nil || ones == 0 || r.Table == c.RoutingTable {
			continue
		}
		p, e := netip.ParsePrefix(r.Dst.String())
		if e == nil && (pool.Contains(p.Addr()) || p.Contains(pool.Addr())) {
			fmt.Printf("warn: effective pool overlaps route %s table %d\n", p, r.Table)
			overlap = true
		}
	}
	if !overlap {
		fmt.Println("ok: effective pool does not overlap non-multitail host addresses/routes")
	}
	for _, p := range c.Profiles {
		d := p.StateDir(config.DefaultStateRoot)
		if st, e := os.Stat(d); e == nil && st.Mode().Perm()&0077 != 0 {
			fmt.Printf("warn: %s permissions are %o (expected 0700)\n", d, st.Mode().Perm())
		}
	}
	if b, e := os.ReadFile("/proc/sys/net/ipv4/conf/all/rp_filter"); e == nil && len(b) > 0 && b[0] != '0' {
		fmt.Printf("warn: rp_filter=%c may block asymmetric multitail replies; set an appropriate loose/disabled policy\n", b[0])
	} else {
		fmt.Println("ok: global rp_filter is disabled")
	}
	if out, e := exec.Command("ip", "-o", "rule", "show").Output(); e == nil {
		for _, l := range bytes.Split(out, []byte{'\n'}) {
			if bytes.HasPrefix(l, []byte("526")) {
				fmt.Printf("warn: reserved multitail ip-rule priority occupied: %s\n", l)
			}
		}
	}
	if _, e := os.Stat("/sys/class/net/tailscale0"); e == nil {
		fmt.Println("warn: tailscale0 exists; v1 daemon will refuse to start")
	}
}
func newID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		die("random ID: %v", e)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
