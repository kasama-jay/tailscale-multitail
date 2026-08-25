// Package runtime owns embedded tsnet profile lifecycle for the daemon.
package runtime

import (
	"context"
	"fmt"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jay/tailscale-multitail/internal/config"
	"github.com/jay/tailscale-multitail/internal/packettun"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"
)

type ProfileStatus struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Hostname  string       `json:"hostname"`
	State     string       `json:"state"`
	IPs       []netip.Addr `json:"ips,omitempty"`
	DNSName   string       `json:"dns_name,omitempty"`
	PeerCount int          `json:"peer_count"`
	Error     string       `json:"error,omitempty"`
}

type profile struct {
	cfg    config.Profile
	server *tsnet.Server
	tun    *packettun.Device
	mu     sync.RWMutex
	status ProfileStatus
}

type Supervisor struct {
	cfg       config.Config
	stateRoot string
	ctx       context.Context
	cancel    context.CancelFunc
	profiles  []*profile
	wg        sync.WaitGroup
}

func New(cfg config.Config, stateRoot string) (*Supervisor, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if stateRoot == "" {
		stateRoot = config.DefaultStateRoot
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Supervisor{cfg: cfg, stateRoot: stateRoot, ctx: ctx, cancel: cancel}, nil
}

// Start initializes every configured profile in config order. Auth keys are
// read only from TAILSCALE_AUTH_KEY_<UPPERCASE_PROFILE_NAME>; they are never
// stored in config, state, logs, or status output.
func (s *Supervisor) Start(ctx context.Context) error {
	if err := os.MkdirAll(s.stateRoot, 0700); err != nil {
		return fmt.Errorf("create state root: %w", err)
	}
	if err := os.Chmod(s.stateRoot, 0700); err != nil {
		return fmt.Errorf("secure state root: %w", err)
	}
	for _, pc := range s.cfg.Profiles {
		p, err := s.startProfile(ctx, pc)
		if err != nil {
			s.Close()
			return err
		}
		s.profiles = append(s.profiles, p)
	}
	return nil
}

func (s *Supervisor) startProfile(ctx context.Context, pc config.Profile) (*profile, error) {
	dir := pc.StateDir(s.stateRoot)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("profile %q state dir: %w", pc.Name, err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, fmt.Errorf("profile %q secure state dir: %w", pc.Name, err)
	}
	p := &profile{cfg: pc, tun: packettun.New("mt-"+pc.Name, s.cfg.MTU, 256), status: ProfileStatus{ID: pc.ID, Name: pc.Name, Hostname: pc.Hostname, State: "Starting"}}
	p.server = &tsnet.Server{Dir: dir, Hostname: pc.Hostname, ControlURL: pc.ControlURL, AdvertiseTags: append([]string(nil), pc.AdvertiseTags...), Tun: p.tun,
		UserLogf: func(f string, a ...any) { log.Printf("profile=%s: "+f, append([]any{pc.Name}, a...)...) }}
	p.server.AuthKey = os.Getenv(authKeyEnv(pc.Name))
	if err := p.server.Start(); err != nil {
		p.tun.Close()
		return nil, fmt.Errorf("start profile %q: %w", pc.Name, err)
	}
	if p.server.AuthKey != "" {
		upCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		if _, err := p.server.Up(upCtx); err != nil {
			p.server.Close()
			return nil, fmt.Errorf("authenticate profile %q: %w", pc.Name, err)
		}
	}
	if err := p.refresh(); err != nil {
		p.server.Close()
		return nil, fmt.Errorf("profile %q status: %w", pc.Name, err)
	}
	s.wg.Add(1)
	go func() { defer s.wg.Done(); p.watch(s.ctx) }()
	return p, nil
}

func authKeyEnv(name string) string { return "TAILSCALE_AUTH_KEY_" + strings.ToUpper(name) }

func (p *profile) refresh() error {
	lc, err := p.server.LocalClient()
	if err != nil {
		return err
	}
	st, err := lc.Status(context.Background())
	if err != nil {
		return err
	}
	p.setStatus(st, "")
	return nil
}
func (p *profile) setStatus(st *ipnstate.Status, failure string) {
	ps := ProfileStatus{ID: p.cfg.ID, Name: p.cfg.Name, Hostname: p.cfg.Hostname, State: st.BackendState, IPs: append([]netip.Addr(nil), st.TailscaleIPs...), PeerCount: len(st.Peer), Error: failure}
	if st.Self != nil {
		ps.DNSName = st.Self.DNSName
	}
	p.mu.Lock()
	p.status = ps
	p.mu.Unlock()
}
func (p *profile) watch(ctx context.Context) {
	lc, err := p.server.LocalClient()
	if err != nil {
		return
	}
	w, err := lc.WatchIPNBus(ctx, ipn.NotifyInitialStatus|ipn.NotifyPeerChanges)
	if err != nil {
		return
	}
	defer w.Close()
	for {
		n, err := w.Next()
		if err != nil {
			return
		}
		if n.ErrMessage != nil {
			p.mu.Lock()
			p.status.Error = *n.ErrMessage
			p.mu.Unlock()
			continue
		}
		if n.InitialStatus != nil {
			p.setStatus(n.InitialStatus, "")
			continue
		}
		if len(n.PeersChanged) > 0 || len(n.PeersRemoved) > 0 || n.State != nil || n.SelfChange != nil {
			_ = p.refresh()
		}
	}
}
func (s *Supervisor) Status() []ProfileStatus {
	out := make([]ProfileStatus, 0, len(s.profiles))
	for _, p := range s.profiles {
		p.mu.RLock()
		out = append(out, p.status)
		p.mu.RUnlock()
	}
	return out
}
func (s *Supervisor) Close() {
	s.cancel()
	for _, p := range s.profiles {
		_ = p.server.Close()
	}
	s.wg.Wait()
}
func AuthKeyEnv(name string) string   { return authKeyEnv(name) }
func StateDir(root, id string) string { return filepath.Join(root, id) }
