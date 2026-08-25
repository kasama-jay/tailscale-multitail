// Package config implements the strict, versioned v1 configuration format.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"
)

const Version = 1
const DefaultPath = "/etc/tailscale-multitail/config.yaml"
const DefaultStateRoot = "/var/lib/tailscale-multitail"

var nameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

type Config struct {
	Version           int       `yaml:"version"`
	Interface         string    `yaml:"interface"`
	RoutingTable      int       `yaml:"routing_table"`
	MTU               int       `yaml:"mtu"`
	EffectiveIPv4CIDR string    `yaml:"effective_ipv4_cidr"`
	Profiles          []Profile `yaml:"profiles"`
}
type Profile struct {
	ID            string   `yaml:"id"`
	Name          string   `yaml:"name"`
	Hostname      string   `yaml:"hostname"`
	ControlURL    string   `yaml:"control_url,omitempty"`
	AdvertiseTags []string `yaml:"advertise_tags,omitempty"`
}

func Default() Config {
	return Config{Version: Version, Interface: "multitail0", RoutingTable: 552, MTU: 1280, EffectiveIPv4CIDR: "10.192.0.0/16", Profiles: []Profile{}}
}
func (p Profile) StateDir(root string) string {
	if root == "" {
		root = DefaultStateRoot
	}
	return filepath.Join(root, p.ID)
}
func Load(path string) (Config, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return Config{}, e
	}
	return Parse(b)
}
func Parse(b []byte) (Config, error) {
	var c Config
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	if e := d.Decode(&c); e != nil {
		return c, fmt.Errorf("invalid config: %w", e)
	}
	if e := c.Validate(); e != nil {
		return c, e
	}
	return c, nil
}
func (c Config) Validate() error {
	if c.Version != Version {
		return fmt.Errorf("unsupported config version %d (want %d)", c.Version, Version)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`).MatchString(c.Interface) {
		return fmt.Errorf("invalid interface %q", c.Interface)
	}
	if c.RoutingTable < 1 || c.RoutingTable > 2147483647 || c.RoutingTable == 52 {
		return fmt.Errorf("routing_table must be 1..2147483647 and not 52")
	}
	if c.MTU < 576 || c.MTU > 9000 {
		return fmt.Errorf("mtu must be 576..9000")
	}
	p, e := netip.ParsePrefix(c.EffectiveIPv4CIDR)
	if e != nil || !p.Addr().Is4() {
		return fmt.Errorf("effective_ipv4_cidr must be an IPv4 CIDR")
	}
	if p.Bits() > 30 {
		return errors.New("effective_ipv4_cidr needs at least four addresses")
	}
	seenName, seenID, seenHost := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i, x := range c.Profiles {
		if x.ID == "" || x.Name == "" || x.Hostname == "" {
			return fmt.Errorf("profiles[%d]: id, name, and hostname are required", i)
		}
		if !nameRE.MatchString(x.Name) {
			return fmt.Errorf("profiles[%d]: invalid name %q", i, x.Name)
		}
		n := strings.ToUpper(x.Name)
		if seenName[n] {
			return fmt.Errorf("duplicate profile name %q (case-insensitive)", x.Name)
		}
		seenName[n] = true
		if seenID[x.ID] {
			return fmt.Errorf("duplicate profile id %q", x.ID)
		}
		seenID[x.ID] = true
		h := strings.ToLower(x.Hostname)
		if seenHost[h] {
			return fmt.Errorf("duplicate hostname %q", x.Hostname)
		}
		seenHost[h] = true
		if x.ControlURL != "" {
			u, e := url.Parse(x.ControlURL)
			if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
				return fmt.Errorf("profile %q: control_url must be an HTTPS URL", x.Name)
			}
		}
		for _, t := range x.AdvertiseTags {
			if !regexp.MustCompile(`^tag:[A-Za-z0-9][A-Za-z0-9_-]*$`).MatchString(t) {
				return fmt.Errorf("profile %q: invalid advertised tag %q", x.Name, t)
			}
		}
	}
	return nil
}
func (c Config) Marshal() ([]byte, error) {
	c.Profiles = append([]Profile(nil), c.Profiles...)
	return yaml.Marshal(c)
}
func WriteAtomic(path string, c Config) error {
	if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		return e
	}
	lock, e := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); e != nil {
		return e
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if e := c.Validate(); e != nil {
		return e
	}
	b, e := c.Marshal()
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		return e
	}
	tmp, e := os.CreateTemp(filepath.Dir(path), ".config-*")
	if e != nil {
		return e
	}
	n := tmp.Name()
	defer os.Remove(n)
	if e = tmp.Chmod(0640); e == nil {
		_, e = tmp.Write(b)
	}
	if e == nil {
		e = tmp.Sync()
	}
	if e2 := tmp.Close(); e == nil {
		e = e2
	}
	if e != nil {
		return e
	}
	return os.Rename(n, path)
}
func SortedProfileNames(c Config) []string {
	r := make([]string, len(c.Profiles))
	for i, p := range c.Profiles {
		r[i] = p.Name
	}
	sort.Strings(r)
	return r
}
