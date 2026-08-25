package mux

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

func TestRawFlowKeyIsBidirectional(t *testing.T) {
	out := pkt(netip.MustParseAddr("10.192.0.1"), netip.MustParseAddr("100.1.2.3"), 1234, 8000)
	in := pkt(netip.MustParseAddr("100.1.2.3"), netip.MustParseAddr("100.9.9.9"), 8000, 1234)
	if a, b := flowKey("p", out, false), flowKey("p", in, true); a != b {
		t.Fatalf("%q != %q", a, b)
	}
}
func pkt(src, dst netip.Addr, sp, dp uint16) []byte {
	p := make([]byte, 40)
	p[0] = 0x45
	p[9] = 6
	binary.BigEndian.PutUint16(p[2:4], 40)
	copy(p[12:16], src.AsSlice())
	copy(p[16:20], dst.AsSlice())
	binary.BigEndian.PutUint16(p[20:22], sp)
	binary.BigEndian.PutUint16(p[22:24], dp)
	return p
}
