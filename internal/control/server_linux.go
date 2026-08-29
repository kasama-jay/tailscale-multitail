//go:build linux

package control

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Request struct {
	Op         string `json:"op"`
	Profile    string `json:"profile,omitempty"`
	AuthKey    string `json:"auth_key,omitempty"`
	ConfigYAML string `json:"config_yaml,omitempty"`
}

type Response struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Result  any    `json:"result,omitempty"`
	Restart bool   `json:"restart,omitempty"`
}

// Caller is the kernel-authenticated Unix peer credential for a request.
// Root callers have UID 0; non-root callers are authorized only if they are a
// member of the daemon's configured management group.
type Caller struct {
	UID  uint32
	GID  uint32
	PID  int32
	Root bool
}

type Handler func(Caller, Request) Response

type Server struct {
	l          *net.UnixListener
	path       string
	allowedGID uint32
	handler    Handler
	sem        chan struct{}
	wg         sync.WaitGroup
}

func Listen(path string, allowedGID uint32, h Handler) (*Server, error) {
	if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		return nil, e
	}

	if e := os.Remove(path); e != nil && !os.IsNotExist(e) {
		return nil, e
	}

	l, e := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if e != nil {
		return nil, e
	}

	if e = os.Chmod(path, 0660); e != nil {
		l.Close()
		return nil, e
	}

	if e = os.Chown(path, 0, int(allowedGID)); e != nil {
		l.Close()
		return nil, e
	}

	s := &Server{
		l:          l,
		path:       path,
		allowedGID: allowedGID,
		handler:    h,
		sem:        make(chan struct{}, 64),
	}
	s.wg.Add(1)
	go s.accept()

	return s, nil
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		c, e := s.l.AcceptUnix()
		if e != nil {
			return
		}
		select {
		case s.sem <- struct{}{}:
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				defer func() { <-s.sem }()
				defer c.Close()
				s.handle(c)
			}()
		default:
			_ = c.Close()
		}
	}
}

func (s *Server) handle(c *net.UnixConn) {
	caller, ok := authorized(c, s.allowedGID)
	if !ok {
		return
	}

	if e := c.SetReadDeadline(time.Now().Add(5 * time.Second)); e != nil {
		return
	}

	dec := json.NewDecoder(bufio.NewReader(io.LimitReader(c, 1<<20)))
	dec.DisallowUnknownFields()
	var r Request
	if e := dec.Decode(&r); e != nil {
		return
	}

	if !validRequest(r) {
		return
	}

	if e := c.SetReadDeadline(time.Time{}); e != nil {
		return
	}

	out := s.handler(caller, r)
	r.AuthKey = ""
	if e := c.SetWriteDeadline(time.Now().Add(5 * time.Second)); e != nil {
		return
	}

	err := json.NewEncoder(c).Encode(out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "control: failed to write response: %v\n", err)
	}

}

func validRequest(r Request) bool {
	if len(r.Op) == 0 || len(r.Op) > 32 || len(r.Profile) > 256 {
		return false
	}

	if len(r.AuthKey) > 16<<10 || len(r.ConfigYAML) > 1<<20 {
		return false
	}

	if r.AuthKey != "" && r.Op != "login" {
		return false
	}

	if r.ConfigYAML != "" && r.Op != "config_write" {
		return false
	}

	return true
}

func (s *Server) Close() error {
	e := s.l.Close()
	s.wg.Wait()
	_ = os.Remove(s.path)
	return e
}

func authorized(c *net.UnixConn, gid uint32) (Caller, bool) {
	raw, e := c.SyscallConn()
	if e != nil {
		return Caller{}, false
	}

	var cred *syscall.Ucred
	_ = raw.Control(func(fd uintptr) {
		cred, _ = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if cred == nil {
		return Caller{}, false
	}

	caller := Caller{UID: cred.Uid, GID: cred.Gid, PID: cred.Pid, Root: cred.Uid == 0}
	if caller.Root || cred.Gid == gid {
		return caller, true
	}

	b, e := os.ReadFile(fmt.Sprintf("/proc/%d/status", cred.Pid))
	if e != nil {
		return Caller{}, false
	}

	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "Groups:") {
			continue
		}
		for _, g := range strings.Fields(strings.TrimPrefix(line, "Groups:")) {
			n, e := strconv.ParseUint(g, 10, 32)
			if e == nil && uint32(n) == gid {
				return caller, true
			}
		}
	}

	return Caller{}, false
}

func ClientRequest(path string, req Request) (Response, error) {
	c, e := net.Dial("unix", path)
	if e != nil {
		return Response{}, e
	}
	defer c.Close()

	if e = json.NewEncoder(c).Encode(req); e != nil {
		return Response{}, e
	}

	req.AuthKey = ""
	var r Response
	e = json.NewDecoder(io.LimitReader(c, 4<<20)).Decode(&r)
	if e != nil {
		return r, e
	}

	if !r.OK {
		return r, errors.New(r.Error)
	}

	return r, nil
}

func Client(path, op string) (Response, error) {
	return ClientRequest(path, Request{Op: op})
}
