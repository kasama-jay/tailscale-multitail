package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/jay/tailscale-multitail/internal/config"
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
func newID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		die("random ID: %v", e)
	}
	return hex.EncodeToString(b)
}
