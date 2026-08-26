package mux

import (
	"encoding/binary"
	"net/netip"
	"testing"
	"time"

	"github.com/jay/tailscale-multitail/internal/packettun"
	"github.com/jay/tailscale-multitail/internal/runtime"
)

func TestRawFlowKeyIsBidirectional(t *testing.T) {
	out := pkt(netip.MustParseAddr("10.192.0.1"), netip.MustParseAddr("100.1.2.3"), 1234, 8000)
	in := pkt(netip.MustParseAddr("100.1.2.3"), netip.MustParseAddr("100.9.9.9"), 8000, 1234)
	if a, b := flowKey("p", out, false), flowKey("p", in, true); a != b {
		t.Fatalf("%q != %q", a, b)
	}
}
func TestUpdatePurgesWithdrawnProfileState(t *testing.T) {
	tun := packettun.New("profile-test", 1280, 1)
	defer tun.Close()

	engine, err := New(
		nil,
		netip.MustParseAddr("10.192.0.1"),
		nil,
		nil,
		[]runtime.DatapathProfile{{
			ID:       "withdrawn",
			SelfIPv4: netip.MustParseAddr("100.1.2.3"),
			Tun:      tun,
		}},
	)
	if err != nil {
		t.Fatal(err)
	}

	engine.flows["withdrawn/6/100.2.3.4/22/12345"] = flowState{seen: time.Now(), proto: 6}
	engine.fragments["withdrawn/6/a/b/1"] = fragmentState{seen: time.Now()}
	engine.Update(nil, nil, nil)

	stats := engine.Stats()
	if stats.Flows != 0 || stats.Fragments != 0 {
		t.Fatalf("retained withdrawn profile state: %+v", stats)
	}

	if stats.PurgedFlows != 1 || stats.PurgedFragments != 1 {
		t.Fatalf("unexpected purge counters: %+v", stats)
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
