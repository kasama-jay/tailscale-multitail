//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/netip"
	"os"
	"testing"
	"time"

	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
)

// TestExitNodeCustomTun probes upstream tsnet only. It creates a temporary
// profile and does not change the host TUN, routing, firewall, or DNS. Run it
// with a key for the exit node's tailnet, an approved exit peer's Tailscale
// IPv4, and a public IPv4 UDP echo server under your control.
func TestExitNodeCustomTun(t *testing.T) {
	key := os.Getenv("TSMULTITAIL_EXIT_TEST_AUTHKEY")
	if key == "" {
		t.Skip("set TSMULTITAIL_EXIT_TEST_AUTHKEY, TSMULTITAIL_EXIT_TEST_PEER_IP, and TSMULTITAIL_EXIT_TEST_ECHO_ADDR to run")
	}

	peerIP, err := netip.ParseAddr(os.Getenv("TSMULTITAIL_EXIT_TEST_PEER_IP"))
	if err != nil || !peerIP.Is4() {
		t.Fatal("TSMULTITAIL_EXIT_TEST_PEER_IP must be the exit peer's Tailscale IPv4 address")
	}

	echo, err := netip.ParseAddrPort(os.Getenv("TSMULTITAIL_EXIT_TEST_ECHO_ADDR"))
	if err != nil || !publicIPv4(echo.Addr()) || echo.Port() == 0 {
		t.Fatal("TSMULTITAIL_EXIT_TEST_ECHO_ADDR must be a public IPv4:port of a UDP echo server you control")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	s, tun := start(t, ctx, "exit", key)
	defer tun.Close()
	defer s.Close()

	lc, err := s.LocalClient()
	if err != nil {
		t.Fatal(err)
	}

	peer, err := waitForExitPeer(ctx, lc.Status, peerIP)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := lc.EditPrefs(ctx, &ipn.MaskedPrefs{
		Prefs: ipn.Prefs{
			ExitNodeID: peer.StableID,
			ExitNodeIP: netip.Addr{},
			RouteAll:   false,
		},
		ExitNodeIDSet: true,
		ExitNodeIPSet: true,
		RouteAllSet:   true,
	}); err != nil {
		t.Fatalf("select exit node %s: %v", peerIP, err)
	}

	if err := waitForSelectedExit(ctx, lc.Status, peer.StableID); err != nil {
		t.Fatal(err)
	}

	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}

	self := mustIPv4(t, s)
	const localPort = 49321
	request := exitProbeUDP(self, echo.Addr(), localPort, echo.Port(), nonce[:])
	replies := make(chan exitProbeResult, 1)
	go func() {
		for {
			packet, err := tun.Receive()
			select {
			case replies <- exitProbeResult{packet: packet, err: err}:
			case <-ctx.Done():
				return
			}

			if err != nil {
				return
			}
		}
	}()

	probe := time.NewTicker(time.Second)
	defer probe.Stop()

	for {
		if err := tun.TryInject(request); err != nil {
			t.Fatalf("inject exit-node probe: %v", err)
		}

		select {
		case result := <-replies:
			if result.err != nil {
				t.Fatalf("receive exit-node probe: %v", result.err)
			}

			if err := checkExitProbeReply(result.packet, echo, self, localPort, nonce[:]); err == nil {
				t.Logf("custom TUN received verified UDP echo from %s through exit node %s (%s)", echo, peerIP, peer.StableID)
				return
			}

			t.Logf("ignoring unrelated custom TUN packet (%d bytes)", len(result.packet))
		case <-probe.C:
		case <-ctx.Done():
			t.Fatal("no verified UDP echo through exit node (check peer, ACL, echo server, firewall, and public IP):", ctx.Err())
		}
	}
}

func TestExitProbePacket(t *testing.T) {
	self := netip.MustParseAddr("100.100.1.2")
	echo := netip.MustParseAddrPort("8.8.8.8:41337")
	payload := []byte("exit-node-probe")

	request := exitProbeUDP(self, echo.Addr(), 49321, echo.Port(), payload)
	if checksum(request[:20]) != 0 {
		t.Fatal("invalid outgoing IPv4 checksum")
	}

	pseudo := make([]byte, 12+len(request)-20)
	copy(pseudo, self.AsSlice())
	copy(pseudo[4:], echo.Addr().AsSlice())
	pseudo[9] = 17
	binary.BigEndian.PutUint16(pseudo[10:12], uint16(len(request)-20))
	copy(pseudo[12:], request[20:])
	if checksum(pseudo) != 0 {
		t.Fatal("invalid outgoing UDP checksum")
	}

	reply := exitProbeUDP(echo.Addr(), self, echo.Port(), 49321, payload)
	if err := checkExitProbeReply(reply, echo, self, 49321, payload); err != nil {
		t.Fatal(err)
	}

	reply[len(reply)-1] ^= 1
	if err := checkExitProbeReply(reply, echo, self, 49321, payload); err == nil {
		t.Fatal("accepted a reply with the wrong payload")
	}
}

type exitProbePeer struct {
	StableID tailcfg.StableNodeID
}

func waitForExitPeer(ctx context.Context, status func(context.Context) (*ipnstate.Status, error), ip netip.Addr) (exitProbePeer, error) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		st, err := status(ctx)
		if err != nil {
			return exitProbePeer{}, fmt.Errorf("query profile status: %w", err)
		}

		for _, peer := range st.Peer {
			for _, addr := range peer.TailscaleIPs {
				if addr != ip {
					continue
				}

				if !peer.ExitNodeOption || peer.ID.IsZero() {
					return exitProbePeer{}, fmt.Errorf("peer %s is not an approved exit node with a stable ID", ip)
				}

				return exitProbePeer{StableID: peer.ID}, nil
			}
		}

		select {
		case <-ctx.Done():
			return exitProbePeer{}, fmt.Errorf("exit peer %s not found in profile netmap: %w", ip, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForSelectedExit(ctx context.Context, status func(context.Context) (*ipnstate.Status, error), id tailcfg.StableNodeID) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		st, err := status(ctx)
		if err != nil {
			return fmt.Errorf("query exit-node status: %w", err)
		}

		if st.ExitNodeStatus != nil && st.ExitNodeStatus.ID == id && st.ExitNodeStatus.Online {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("exit node %s did not become selected and online: %w", id, ctx.Err())
		case <-ticker.C:
		}
	}
}

func publicIPv4(ip netip.Addr) bool {
	return ip.Is4() && ip.IsGlobalUnicast() && !ip.IsPrivate() &&
		!ip.IsLoopback() && !ip.IsLinkLocalUnicast() &&
		!netip.MustParsePrefix("100.64.0.0/10").Contains(ip) &&
		!netip.MustParsePrefix("198.18.0.0/15").Contains(ip)
}

func exitProbeUDP(src, dst netip.Addr, sourcePort, destPort uint16, payload []byte) []byte {
	p := make([]byte, 20+8+len(payload))
	p[0], p[8], p[9] = 0x45, 64, 17
	binary.BigEndian.PutUint16(p[2:4], uint16(len(p)))
	copy(p[12:16], src.AsSlice())
	copy(p[16:20], dst.AsSlice())
	binary.BigEndian.PutUint16(p[20:22], sourcePort)
	binary.BigEndian.PutUint16(p[22:24], destPort)
	binary.BigEndian.PutUint16(p[24:26], uint16(len(p)-20))
	copy(p[28:], payload)

	pseudo := make([]byte, 12+len(p)-20)
	copy(pseudo, src.AsSlice())
	copy(pseudo[4:], dst.AsSlice())
	pseudo[9] = 17
	binary.BigEndian.PutUint16(pseudo[10:12], uint16(len(p)-20))
	copy(pseudo[12:], p[20:])
	udpChecksum := checksum(pseudo)
	if udpChecksum == 0 {
		udpChecksum = 0xffff
	}
	binary.BigEndian.PutUint16(p[26:28], udpChecksum)
	binary.BigEndian.PutUint16(p[10:12], checksum(p[:20]))
	return p
}

type exitProbeResult struct {
	packet []byte
	err    error
}

func checkExitProbeReply(p []byte, echo netip.AddrPort, self netip.Addr, localPort uint16, nonce []byte) error {
	if len(p) < 28 || p[0] != 0x45 || p[9] != 17 || int(binary.BigEndian.Uint16(p[2:4])) != len(p) {
		return fmt.Errorf("unexpected packet from custom TUN (want IPv4 UDP echo reply, got %d bytes)", len(p))
	}

	src := netip.AddrFrom4([4]byte(p[12:16]))
	dst := netip.AddrFrom4([4]byte(p[16:20]))
	if src != echo.Addr() || dst != self || binary.BigEndian.Uint16(p[20:22]) != echo.Port() ||
		binary.BigEndian.Uint16(p[22:24]) != localPort ||
		int(binary.BigEndian.Uint16(p[24:26])) != len(p)-20 || !bytes.Equal(p[28:], nonce) {
		return fmt.Errorf("unexpected exit-node reply from %s to %s (want %s to %s, port %d, matching nonce)", src, dst, echo.Addr(), self, localPort)
	}

	return nil
}
