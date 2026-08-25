package config

import "testing"

func TestParseStrictAndValidate(t *testing.T) {
	good := `version: 1
interface: multitail0
routing_table: 552
mtu: 1280
effective_ipv4_cidr: 10.192.0.0/16
profiles:
  - id: abc
    name: Work
    hostname: host-work
    control_url: https://control.example.test
    advertise_tags: [tag:server]
`
	c, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if c.Profiles[0].StateDir("/state") != "/state/abc" {
		t.Fatal("bad state dir")
	}
	for _, bad := range []string{"unknown: x\n" + good, `version: 1
interface: multitail0
routing_table: 52
mtu: 1280
effective_ipv4_cidr: 10.192.0.0/16
profiles: []
`, `version: 1
interface: multitail0
routing_table: 552
mtu: 1280
effective_ipv4_cidr: 10.192.0.0/16
profiles:
- id: a
  name: x
  hostname: h
- id: b
  name: X
  hostname: h2
`} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Fatalf("accepted invalid config: %q", bad)
		}
	}
}
