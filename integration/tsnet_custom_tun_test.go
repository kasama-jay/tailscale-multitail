//go:build integration

package integration

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jay/tailscale-multitail/internal/packettun"
	"tailscale.com/ipn"
	"tailscale.com/tsnet"
)

// TestCustomTunRoundTrip is the Milestone 0.5 feasibility gate. It needs two
// one-off, reusable auth keys for nodes in the same test tailnet. The tailnet
// ACL must permit ICMP between them.
func TestCustomTunRoundTrip(t *testing.T) {
	keyA, keyB := os.Getenv("TSMULTITAIL_TEST_AUTHKEY_A"), os.Getenv("TSMULTITAIL_TEST_AUTHKEY_B")
	if keyA == "" || keyB == "" {
		t.Skip("set TSMULTITAIL_TEST_AUTHKEY_A and TSMULTITAIL_TEST_AUTHKEY_B to run against a real tailnet")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	a, tunA := start(t, ctx, "a", keyA)
	defer a.Close()
	b, tunB := start(t, ctx, "b", keyB)
	defer b.Close()

	ipA := mustIPv4(t, a)
	ipB := mustIPv4(t, b)
	assertPublicLocalAPI(t, ctx, a)
	assertPublicLocalAPI(t, ctx, b)

	if err := tunA.Inject(ipv4ICMPEcho(ipA, ipB, 0x1234)); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-receive(tunB):
		if result.err != nil {
			t.Fatal(result.err)
		}
		got := result.p
		if len(got) < 20 {
			t.Fatalf("B received short packet: %d bytes", len(got))
		}
		if src, dst := netip.AddrFrom4([4]byte(got[12:16])), netip.AddrFrom4([4]byte(got[16:20])); src != ipA || dst != ipB {
			t.Fatalf("B received %s -> %s, want %s -> %s", src, dst, ipA, ipB)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for packet emitted through B custom TUN")
	}

	if err := tunB.Inject(ipv4ICMPEcho(ipB, ipA, 0x5678)); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-receive(tunA):
		if result.err != nil {
			t.Fatal(result.err)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for reverse packet emitted through A custom TUN")
	}
}

func start(t *testing.T, ctx context.Context, suffix, key string) (*tsnet.Server, *packettun.Device) {
	t.Helper()
	tun := packettun.New("feasibility-"+suffix, 1280, 128)
	s := &tsnet.Server{Dir: filepath.Join(t.TempDir(), "state"), Hostname: fmt.Sprintf("multitail-feasibility-%s-%d", suffix, time.Now().UnixNano()), AuthKey: key, Tun: tun}
	if _, err := s.Up(ctx); err != nil {
		t.Fatalf("start %s: %v", suffix, err)
	}
	return s, tun
}

func mustIPv4(t *testing.T, s *tsnet.Server) netip.Addr {
	t.Helper()
	ip, _ := s.TailscaleIPs()
	if !ip.Is4() {
		t.Fatalf("server has no IPv4 Tailscale IP: %v", ip)
	}
	return ip
}

func assertPublicLocalAPI(t *testing.T, ctx context.Context, s *tsnet.Server) {
	t.Helper()
	lc, err := s.LocalClient()
	if err != nil {
		t.Fatal(err)
	}
	status, err := lc.Status(ctx)
	if err != nil {
		t.Fatalf("LocalClient.Status: %v", err)
	}
	if status.Self == nil || status.Self.DNSName == "" {
		t.Fatal("LocalClient.Status did not expose this profile's MagicDNS name")
	}
	if _, _, err := lc.QueryDNS(ctx, status.Self.DNSName, "A"); err != nil {
		t.Fatalf("LocalClient.QueryDNS(%q, A): %v", status.Self.DNSName, err)
	}
	watchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	w, err := lc.WatchIPNBus(watchCtx, ipn.NotifyInitialState)
	if err != nil {
		t.Fatalf("LocalClient.WatchIPNBus: %v", err)
	}
	defer w.Close()
	if _, err := w.Next(); err != nil {
		t.Fatalf("IPN bus initial state: %v", err)
	}
}

func receive(d *packettun.Device) <-chan struct {
	p   []byte
	err error
} {
	ch := make(chan struct {
		p   []byte
		err error
	}, 1)
	go func() {
		p, err := d.Receive()
		ch <- struct {
			p   []byte
			err error
		}{p, err}
	}()
	return ch
}

func ipv4ICMPEcho(src, dst netip.Addr, id uint16) []byte {
	p := make([]byte, 28)
	p[0], p[8], p[9] = 0x45, 64, 1
	binary.BigEndian.PutUint16(p[2:4], uint16(len(p)))
	copy(p[12:16], src.AsSlice())
	copy(p[16:20], dst.AsSlice())
	p[20], p[21] = 8, 0 // ICMP echo request
	binary.BigEndian.PutUint16(p[24:26], id)
	binary.BigEndian.PutUint16(p[22:24], checksum(p[20:]))
	binary.BigEndian.PutUint16(p[10:12], checksum(p[:20]))
	return p
}

func checksum(p []byte) uint16 {
	var sum uint32
	for len(p) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(p))
		p = p[2:]
	}
	if len(p) == 1 {
		sum += uint32(p[0]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}
