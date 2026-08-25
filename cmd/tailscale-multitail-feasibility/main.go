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
	"tailscale.com/ipn"
	"tailscale.com/tsnet"
)

var version = "devel"

func main() {
	var stateDir string
	var timeout time.Duration
	var showVersion bool
	flag.StringVar(&stateDir, "state-dir", "", "directory for temporary profile state; retained when specified")
	flag.DurationVar(&timeout, "timeout", 2*time.Minute, "overall feasibility-check timeout")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Parse()
	if showVersion {
		fmt.Println(version)
		return
	}
	if err := run(stateDir, timeout); err != nil {
		log.Fatal(err)
	}
}

func run(stateDir string, timeout time.Duration) error {
	keyA, keyB := os.Getenv("TSMULTITAIL_TEST_AUTHKEY_A"), os.Getenv("TSMULTITAIL_TEST_AUTHKEY_B")
	if keyA == "" || keyB == "" {
		return errors.New("set TSMULTITAIL_TEST_AUTHKEY_A and TSMULTITAIL_TEST_AUTHKEY_B; keys are accepted only through the environment")
	}
	if timeout <= 0 {
		return errors.New("timeout must be positive")
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
	if err := checkLocalAPI(ctx, a); err != nil {
		return fmt.Errorf("profile A LocalAPI: %w", err)
	}
	if err := checkLocalAPI(ctx, b); err != nil {
		return fmt.Errorf("profile B LocalAPI: %w", err)
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
	fmt.Printf("PASS: custom TUN round trip succeeded (%s <-> %s); LocalAPI Status, QueryDNS, and WatchIPNBus succeeded\n", ipA, ipB)
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

func checkLocalAPI(ctx context.Context, s *tsnet.Server) error {
	lc, err := s.LocalClient()
	if err != nil {
		return err
	}
	status, err := lc.Status(ctx)
	if err != nil {
		return err
	}
	if status.Self == nil || status.Self.DNSName == "" {
		return errors.New("status does not expose a MagicDNS name")
	}
	if _, _, err := lc.QueryDNS(ctx, status.Self.DNSName, "A"); err != nil {
		return fmt.Errorf("QueryDNS: %w", err)
	}
	watchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	watcher, err := lc.WatchIPNBus(watchCtx, ipn.NotifyInitialState)
	if err != nil {
		return err
	}
	defer watcher.Close()
	_, err = watcher.Next()
	return err
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
