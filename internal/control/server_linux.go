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
type Handler func(Request) Response
type Server struct {
	l          *net.UnixListener
	path       string
	allowedGID uint32
	handler    Handler
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
	s := &Server{l: l, path: path, allowedGID: allowedGID, handler: h}
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
		s.wg.Add(1)
		go func() { defer s.wg.Done(); defer c.Close(); s.handle(c) }()
	}
}
func (s *Server) handle(c *net.UnixConn) {
	if !authorized(c, s.allowedGID) {
		return
	}
	dec := json.NewDecoder(bufio.NewReader(io.LimitReader(c, 1<<20)))
	dec.DisallowUnknownFields()
	var r Request
	if e := dec.Decode(&r); e != nil {
		return
	}
	out := s.handler(r)
	r.AuthKey = ""
	_ = json.NewEncoder(c).Encode(out)
}
func (s *Server) Close() error { e := s.l.Close(); s.wg.Wait(); _ = os.Remove(s.path); return e }
func authorized(c *net.UnixConn, gid uint32) bool {
	raw, e := c.SyscallConn()
	if e != nil {
		return false
	}
	var cred *syscall.Ucred
	_ = raw.Control(func(fd uintptr) { cred, _ = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED) })
	if cred == nil {
		return false
	}
	if cred.Uid == 0 || cred.Gid == gid {
		return true
	}
	b, e := os.ReadFile(fmt.Sprintf("/proc/%d/status", cred.Pid))
	if e != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "Groups:") {
			continue
		}
		for _, g := range strings.Fields(strings.TrimPrefix(line, "Groups:")) {
			n, e := strconv.ParseUint(g, 10, 32)
			if e == nil && uint32(n) == gid {
				return true
			}
		}
	}
	return false
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
func Client(path, op string) (Response, error) { return ClientRequest(path, Request{Op: op}) }
