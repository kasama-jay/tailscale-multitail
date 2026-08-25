package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/jay/tailscale-multitail/internal/config"
	"github.com/jay/tailscale-multitail/internal/control"
)

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
		configCmd(p, a[1:])
	case "profiles":
		profilesCmd(p, a[1:])
	case "doctor":
		doctor(p)
	case "status":
		r, e := control.Client(socket, "status")
		if e != nil {
			die("status: %v", e)
		}
		json.NewEncoder(os.Stdout).Encode(r.Status)
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
func configCmd(p string, a []string) {
	if len(a) == 0 {
		die("usage: config {init|show|set}")
	}
	switch a[0] {
	case "init":
		if _, e := os.Stat(p); e == nil {
			die("%s already exists (use --force not yet supported)", p)
		}
		if e := config.WriteAtomic(p, config.Default()); e != nil {
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
		if e = config.WriteAtomic(p, c); e != nil {
			die("%v", e)
		}
	default:
		die("unknown config command %q", a[0])
	}
}
func profilesCmd(p string, a []string) {
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
		if e := config.WriteAtomic(p, c); e != nil {
			die("%v", e)
		}
		fmt.Println(x.ID)
	default:
		die("unknown profiles command %q", a[0])
	}
}
func doctor(path string) {
	c, err := config.Load(path)
	if err != nil {
		die("config: %v", err)
	}
	fmt.Println("ok: strict configuration")
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
	return hex.EncodeToString(b)
}
