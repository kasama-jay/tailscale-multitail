// Package inventory maintains the ordered aggregate view of active profiles.
package inventory

import (
	"net/netip"
	"sort"
)

type Kind string

const (
	Node    Kind = "node"
	Service Kind = "service"
)

type Target struct {
	ProfileID   string     `json:"profile_id"`
	ProfileName string     `json:"profile_name"`
	Order       int        `json:"order"`
	Kind        Kind       `json:"kind"`
	ID          string     `json:"id"`
	FQDN        string     `json:"fqdn,omitempty"`
	Online      bool       `json:"online"`
	CanonicalIP netip.Addr `json:"canonical_ip"`
}
type Profile struct {
	ID      string
	Name    string
	Order   int
	Targets []Target
}
type Snapshot struct {
	Targets   []Target                `json:"targets"`
	Canonical map[netip.Addr][]Target `json:"-"`
	FQDN      map[string][]Target     `json:"-"`
}

func Key(t Target) string {
	return t.ProfileID + "/" + string(t.Kind) + "/" + t.ID + "/" + t.CanonicalIP.String()
}

func Build(profiles []Profile) Snapshot {
	s := Snapshot{Canonical: map[netip.Addr][]Target{}, FQDN: map[string][]Target{}}
	for _, p := range profiles {
		for _, t := range p.Targets {
			t.ProfileID = p.ID
			t.ProfileName = p.Name
			t.Order = p.Order
			s.Targets = append(s.Targets, t)
			s.Canonical[t.CanonicalIP] = append(s.Canonical[t.CanonicalIP], t)
			if t.FQDN != "" {
				s.FQDN[t.FQDN] = append(s.FQDN[t.FQDN], t)
			}
		}
	}
	sort.Slice(s.Targets, func(i, j int) bool {
		a, b := s.Targets[i], s.Targets[j]
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.CanonicalIP.Less(b.CanonicalIP)
	})
	return s
}

// ResolveRaw implements v1's intentional ordered first-match policy.
func (s Snapshot) ResolveRaw(ip netip.Addr) (Target, bool) {
	v := s.Canonical[ip]
	if len(v) == 0 {
		return Target{}, false
	}
	sort.SliceStable(v, func(i, j int) bool { return v[i].Order < v[j].Order })
	return v[0], true
}
func (s Snapshot) Collisions() map[netip.Addr][]Target {
	r := map[netip.Addr][]Target{}
	for ip, v := range s.Canonical {
		if len(v) > 1 {
			r[ip] = append([]Target(nil), v...)
		}
	}
	return r
}
