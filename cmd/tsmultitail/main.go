package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"flag"
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

type stringList []string

func (v *stringList) String() string     { return strings.Join(*v, ",") }
func (v *stringList) Set(s string) error { *v = append(*v, s); return nil }

func die(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...); os.Exit(2) }
func parse(fs *flag.FlagSet, args []string) {
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			os.Exit(0)
		}
		die("%v", err)
	}
}
func main() {
	fs := flag.NewFlagSet("tsmultitail", flag.ContinueOnError)
	p := fs.String("config", config.DefaultPath, "path to the YAML configuration")
	socket := fs.String("socket", "/run/tailscale-multitail/control.sock", "daemon control socket")
	showVersion := fs.Bool("version", false, "print version and exit")
	parse(fs, os.Args[1:])
	a := fs.Args()
	if *showVersion {
		if len(a) != 0 {
			die("--version does not accept a command")
		}
		fmt.Println(version)
		return
	}
	if len(a) == 0 {
		die("usage: tsmultitail [--config PATH] [--socket PATH] {validate|example-config|config|profiles}")
	}
	switch a[0] {
	case "validate":
		c, e := config.Load(*p)
		if e != nil {
			die("%v", e)
		}
		fmt.Printf("valid: %s (%d profiles)\n", *p, len(c.Profiles))
	case "example-config":
		b, _ := config.Default().Marshal()
		os.Stdout.Write(b)
	case "config":
		configCmd(*socket, *p, a[1:])
	case "profiles":
		if len(a) > 1 && (a[1] == "login" || a[1] == "logout") {
			profileLive(*socket, a[1:])
			return
		}
		profilesCmd(*socket, *p, a[1:])
	case "doctor":
		doctor(*p)
	case "status":
		r, e := control.Client(*socket, "status")
		if e != nil {
			die("status: %v", e)
		}
		json.NewEncoder(os.Stdout).Encode(r.Result)
	case "daemon":
		if len(a) != 2 || a[1] != "restart" {
			die("usage: daemon restart")
		}
		if _, e := control.Client(*socket, "restart"); e != nil {
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
		if len(a) < 2 {
			die("usage: profiles add NAME --hostname HOSTNAME [--control-url URL]")
		}
		fs := flag.NewFlagSet("profiles add", flag.ContinueOnError)
		hostname := fs.String("hostname", "", "profile hostname")
		controlURL := fs.String("control-url", "", "HTTPS control URL")
		var tags stringList
		fs.Var(&tags, "advertise-tag", "advertised tag (repeatable)")
		parse(fs, a[2:])
		if len(fs.Args()) != 0 {
			die("unexpected arguments: %s", strings.Join(fs.Args(), " "))
		}
		if *hostname == "" {
			die("--hostname is required")
		}
		x := config.Profile{Name: a[1], Hostname: *hostname, ControlURL: *controlURL, AdvertiseTags: tags, ID: newID()}
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
		fs := flag.NewFlagSet("profiles login", flag.ContinueOnError)
		authKeyStdin := fs.Bool("auth-key-stdin", false, "read auth key from standard input")
		parse(fs, a[2:])
		if len(fs.Args()) != 0 {
			die("unexpected arguments: %s", strings.Join(fs.Args(), " "))
		}
		key := ""
		if *authKeyStdin {
			b, e := io.ReadAll(io.LimitReader(os.Stdin, 16<<10))
			if e != nil {
				die("read auth key: %v", e)
			}
			key = strings.TrimSpace(string(b))
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
		fs := flag.NewFlagSet("profiles logout", flag.ContinueOnError)
		yes := fs.Bool("yes", false, "confirm logout")
		parse(fs, a[2:])
		if len(fs.Args()) != 0 {
			die("unexpected arguments: %s", strings.Join(fs.Args(), " "))
		}
		if !*yes {
			die("logout requires --yes")
		}
		if _, e := control.ClientRequest(socket, control.Request{Op: "logout", Profile: a[1]}); e != nil {
			die("logout: %v", e)
		}
	default:
		die("unknown live profile command %q", a[0])
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
