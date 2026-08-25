package mux

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

func TestFragmentAddressTranslation(t *testing.T) {
	p := make([]byte, 28)
	p[0] = 0x45
	p[9] = 17
	binary.BigEndian.PutUint16(p[2:4], 28)
	binary.BigEndian.PutUint16(p[4:6], 42)
	binary.BigEndian.PutUint16(p[6:8], 0x2000)
	copy(p[12:16], netip.MustParseAddr("10.0.0.1").AsSlice())
	copy(p[16:20], netip.MustParseAddr("10.0.0.2").AsSlice())
	binary.BigEndian.PutUint16(p[20:22], 1234)
	binary.BigEndian.PutUint16(p[22:24], 53)
	binary.BigEndian.PutUint16(p[26:28], 0x1234)
	old := binary.BigEndian.Uint16(p[26:28])
	if e := rewrite(p, netip.MustParseAddr("100.1.1.1"), netip.MustParseAddr("100.2.2.2"), 17); e != nil {
		t.Fatal(e)
	}
	if binary.BigEndian.Uint16(p[26:28]) == old {
		t.Fatal("fragment UDP checksum was not incrementally adjusted")
	}
	p2 := append([]byte(nil), p...)
	binary.BigEndian.PutUint16(p2[6:8], 1)
	if e := rewrite(p2, netip.MustParseAddr("10.1.1.1"), netip.MustParseAddr("10.2.2.2"), 17); e != nil {
		t.Fatal(e)
	}
}
