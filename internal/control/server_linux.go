//go:build linux

package control

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

type Request struct {
	Op string `json:"op"`
}
type Response struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Status  any    `json:"status,omitempty"`
	Restart bool   `json:"restart,omitempty"`
}
type Server struct {
	l          *net.UnixListener
	allowedGID uint32
	status     func() any
	stop       func()
	wg         sync.WaitGroup
}

func Listen(path string, allowedGID uint32, status func() any, stop func()) (*Server, error) {
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
	s := &Server{l: l, allowedGID: allowedGID, status: status, stop: stop}
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
	var r Request
	if e := json.NewDecoder(bufio.NewReader(c)).Decode(&r); e != nil {
		return
	}
	out := Response{}
	switch r.Op {
	case "status":
		out.OK = true
		out.Status = s.status()
	case "restart":
		out.OK = true
		out.Restart = true
		go s.stop()
	default:
		out.Error = "unknown operation"
	}
	_ = json.NewEncoder(c).Encode(out)
}
func (s *Server) Close() error { e := s.l.Close(); s.wg.Wait(); return e }
func authorized(c *net.UnixConn, gid uint32) bool {
	raw, e := c.SyscallConn()
	if e != nil {
		return false
	}
	var cred *syscall.Ucred
	_ = raw.Control(func(fd uintptr) { cred, _ = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED) })
	return cred != nil && (cred.Uid == 0 || cred.Gid == gid)
}
func Client(path, op string) (Response, error) {
	c, e := net.Dial("unix", path)
	if e != nil {
		return Response{}, e
	}
	defer c.Close()
	if e = json.NewEncoder(c).Encode(Request{op}); e != nil {
		return Response{}, e
	}
	var r Response
	e = json.NewDecoder(c).Decode(&r)
	if e != nil {
		return r, e
	}
	if !r.OK {
		return r, errors.New(r.Error)
	}
	return r, nil
}
