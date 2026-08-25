// tailscale-multitail-feasibility proves the tsnet custom-TUN integration used
// by the first multitail implementation milestone.
package main

import (
	"context"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/jay/tailscale-multitail/internal/packettun"
	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
)

var version = "devel"

func main() {
	var stateDir, externalPeer string
	var timeout time.Duration
	var showVersion bool
	flag.StringVar(&stateDir, "state-dir", "", "directory for temporary profile state; retained when specified")
	flag.StringVar(&externalPeer, "external-peer", "", "IPv4 address of a separate normal Tailscale node to test the external inbound path")
	flag.DurationVar(&timeout, "timeout", 2*time.Minute, "overall feasibility-check timeout")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Parse()
	if showVersion {
		fmt.Println(version)
		return
	}
	if err := run(stateDir, externalPeer, timeout); err != nil {
		log.Fatal(err)
	}
}

func run(stateDir, externalPeer string, timeout time.Duration) error {
	keyA, keyB := os.Getenv("TSMULTITAIL_TEST_AUTHKEY_A"), os.Getenv("TSMULTITAIL_TEST_AUTHKEY_B")
	if keyA == "" || keyB == "" {
		return errors.New("set TSMULTITAIL_TEST_AUTHKEY_A and TSMULTITAIL_TEST_AUTHKEY_B; keys are accepted only through the environment")
	}
	if timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	var outside netip.Addr
	if externalPeer != "" {
		var err error
		outside, err = netip.ParseAddr(externalPeer)
		if err != nil || !outside.Is4() {
			return fmt.Errorf("external-peer must be an IPv4 address: %q", externalPeer)
		}
	}

	removeState := false
	if stateDir == "" {
		var err error
		stateDir, err = os.MkdirTemp("", "tailscale-multitail-feasibility-")
		if err != nil {
			return fmt.Errorf("create temporary state directory: %w", err)
		}
		removeState = true
	}
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	if err := os.Chmod(stateDir, 0700); err != nil {
		return fmt.Errorf("secure state directory: %w", err)
	}
	if removeState {
		defer os.RemoveAll(stateDir)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	a, tunA, err := start(ctx, filepath.Join(stateDir, "a"), "a", keyA)
	if err != nil {
		return err
	}
	defer a.Close()
	watcher, err := peerWatcher(ctx, a)
	if err != nil {
		return fmt.Errorf("profile A peer watcher: %w", err)
	}
	defer watcher.Close()

	b, tunB, err := start(ctx, filepath.Join(stateDir, "b"), "b", keyB)
	if err != nil {
		return err
	}
	defer b.Close()
	ipA, err := ipv4(a)
	if err != nil {
		return fmt.Errorf("profile A: %w", err)
	}
	ipB, err := ipv4(b)
	if err != nil {
		return fmt.Errorf("profile B: %w", err)
	}
	bStatus, err := status(ctx, b)
	if err != nil {
		return fmt.Errorf("profile B Status: %w", err)
	}
	if bStatus.Self == nil || bStatus.Self.NodeID == 0 {
		return errors.New("profile B Status does not expose its node ID")
	}
	if err := waitForPeerChange(ctx, watcher, bStatus.Self.NodeID); err != nil {
		return fmt.Errorf("profile A did not receive a peer-add update for B: %w", err)
	}
	if err := checkInventory(ctx, a, ipB); err != nil {
		return fmt.Errorf("profile A inventory for B: %w", err)
	}
	if err := checkInventory(ctx, b, ipA); err != nil {
		return fmt.Errorf("profile B inventory for A: %w", err)
	}

	if err := tunA.Inject(echo(ipA, ipB, 0x1234)); err != nil {
		return fmt.Errorf("inject A -> B: %w", err)
	}
	if err := waitFor(ctx, tunB, ipA, ipB); err != nil {
		return fmt.Errorf("A -> B custom-TUN path: %w", err)
	}
	if err := tunB.Inject(echo(ipB, ipA, 0x5678)); err != nil {
		return fmt.Errorf("inject B -> A: %w", err)
	}
	if err := waitFor(ctx, tunA, ipB, ipA); err != nil {
		return fmt.Errorf("B -> A custom-TUN path: %w", err)
	}
	if outside.IsValid() {
		if err := tunA.Inject(echo(ipA, outside, 0x9abc)); err != nil {
			return fmt.Errorf("inject A -> external peer: %w", err)
		}
		if err := waitFor(ctx, tunA, outside, ipA); err != nil {
			return fmt.Errorf("external inbound path (%s -> %s): %w", outside, ipA, err)
		}
	}

	msg := "PASS: custom TUN bidirectional path; peer inventory; peer-change watch; per-profile Status/QueryDNS; and GetServices API succeeded"
	if outside.IsValid() {
		msg += fmt.Sprintf("; external-node ICMP reply succeeded (%s)", outside)
	}
	fmt.Println(msg)
	return nil
}

func start(ctx context.Context, dir, suffix, key string) (*tsnet.Server, *packettun.Device, error) {
	tun := packettun.New("feasibility-"+suffix, 1280, 128)
	s := &tsnet.Server{Dir: dir, Hostname: fmt.Sprintf("multitail-feasibility-%s-%d", suffix, time.Now().UnixNano()), AuthKey: key, Tun: tun}
	if _, err := s.Up(ctx); err != nil {
		tun.Close()
		return nil, nil, fmt.Errorf("start profile %s: %w", suffix, err)
	}
	return s, tun, nil
}

func ipv4(s *tsnet.Server) (netip.Addr, error) {
	ip, _ := s.TailscaleIPs()
	if !ip.Is4() {
		return netip.Addr{}, fmt.Errorf("no IPv4 Tailscale address (%v)", ip)
	}
	return ip, nil
}

func peerWatcher(ctx context.Context, s *tsnet.Server) (*local.IPNBusWatcher, error) {
	lc, err := s.LocalClient()
	if err != nil {
		return nil, err
	}
	w, err := lc.WatchIPNBus(ctx, ipn.NotifyInitialState|ipn.NotifyPeerChanges)
	if err != nil {
		return nil, err
	}
	if _, err := w.Next(); err != nil {
		w.Close()
		return nil, err
	}
	return w, nil
}

func waitForPeerChange(ctx context.Context, w *local.IPNBusWatcher, want tailcfg.NodeID) error {
	for {
		n, err := w.Next()
		if err != nil {
			return err
		}
		for _, peer := range n.PeersChanged {
			if peer.ID == want {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
}

func checkInventory(ctx context.Context, s *tsnet.Server, peerIP netip.Addr) error {
	lc, err := s.LocalClient()
	if err != nil {
		return err
	}
	status, err := status(ctx, s)
	if err != nil {
		return fmt.Errorf("Status: %w", err)
	}
	if status.Self == nil || status.Self.DNSName == "" || status.CurrentTailnet == nil || status.CurrentTailnet.MagicDNSSuffix == "" {
		return errors.New("Status does not expose self MagicDNS inventory")
	}
	if _, _, err := lc.QueryDNS(ctx, status.Self.DNSName, "A"); err != nil {
		return fmt.Errorf("QueryDNS(%q, A): %w", status.Self.DNSName, err)
	}
	peer, ok := findPeer(status, peerIP)
	if !ok {
		return fmt.Errorf("peer %s is absent from Status", peerIP)
	}
	if peer.ID == "" || peer.DNSName == "" || len(peer.TailscaleIPs) == 0 {
		return fmt.Errorf("peer %s inventory is incomplete: stable_id=%q dns=%q ips=%v", peerIP, peer.ID, peer.DNSName, peer.TailscaleIPs)
	}
	services, err := lc.GetServices(ctx)
	if err != nil {
		return fmt.Errorf("GetServices: %w", err)
	}
	fmt.Printf("inventory: peer=%s stable_id=%s dns=%s services=%d\n", peerIP, peer.ID, peer.DNSName, len(services))
	return nil
}

func status(ctx context.Context, s *tsnet.Server) (*ipnstate.Status, error) {
	lc, err := s.LocalClient()
	if err != nil {
		return nil, err
	}
	return lc.Status(ctx)
}

func findPeer(status *ipnstate.Status, want netip.Addr) (*ipnstate.PeerStatus, bool) {
	for _, peer := range status.Peer {
		for _, ip := range peer.TailscaleIPs {
			if ip == want {
				return peer, true
			}
		}
	}
	return nil, false
}

func waitFor(ctx context.Context, d *packettun.Device, wantSrc, wantDst netip.Addr) error {
	type result struct {
		p   []byte
		err error
	}
	ch := make(chan result, 1)
	go func() { p, err := d.Receive(); ch <- result{p, err} }()
	select {
	case r := <-ch:
		if r.err != nil {
			return r.err
		}
		if len(r.p) < 20 {
			return fmt.Errorf("short packet: %d bytes", len(r.p))
		}
		src, dst := netip.AddrFrom4([4]byte(r.p[12:16])), netip.AddrFrom4([4]byte(r.p[16:20]))
		if src != wantSrc || dst != wantDst {
			return fmt.Errorf("got %s -> %s, want %s -> %s", src, dst, wantSrc, wantDst)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func echo(src, dst netip.Addr, id uint16) []byte {
	p := make([]byte, 28)
	p[0], p[8], p[9] = 0x45, 64, 1
	binary.BigEndian.PutUint16(p[2:4], uint16(len(p)))
	copy(p[12:16], src.AsSlice())
	copy(p[16:20], dst.AsSlice())
	p[20] = 8
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
