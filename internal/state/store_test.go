package state

import (
	"path/filepath"
	"testing"
)

func TestLeasePersistenceAndCIDRReset(t *testing.T) {
	d := t.TempDir()
	s, changed, e := Open(d, "10.192.0.0/16")
	if e != nil || changed {
		t.Fatal(e, changed)
	}
	if e = s.Put("a", "10.192.0.3"); e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, changed, e = Open(d, "10.192.0.0/16")
	if e != nil || changed {
		t.Fatal(e, changed)
	}
	m, e := s.Leases()
	if e != nil || m["a"] != "10.192.0.3" {
		t.Fatal(m, e)
	}
	s.Close()
	s, changed, e = Open(filepath.Clean(d), "10.193.0.0/16")
	if e != nil || !changed {
		t.Fatal(e, changed)
	}
	m, e = s.Leases()
	if e != nil || len(m) != 0 {
		t.Fatal(m, e)
	}
	s.Close()
}
