// Package packettun provides the channel-backed tun.Device used between the
// host packet mux and an embedded tsnet profile.
package packettun

import (
	"errors"
	"io"
	"os"
	"sync"

	"github.com/tailscale/wireguard-go/tun"
)

var errClosed = errors.New("channel tun is closed")
var errQueueFull = errors.New("channel tun queue full")

// Device is a bounded, in-process packet TUN. Packets injected through Inject
// are read by tsnet; packets written by tsnet are available through Receive.
// Each boundary owns its packet bytes, so callers may reuse their buffers.
type Device struct {
	name string
	mtu  int

	toEngine   chan []byte
	fromEngine chan []byte
	events     chan tun.Event
	done       chan struct{}
	closeOnce  sync.Once
}

// New creates an up channel-backed TUN. queueDepth bounds backpressure on
// either side of the packet mux.
func New(name string, mtu, queueDepth int) *Device {
	if queueDepth < 1 {
		queueDepth = 1
	}
	d := &Device{
		name: name, mtu: mtu,
		toEngine: make(chan []byte, queueDepth), fromEngine: make(chan []byte, queueDepth),
		events: make(chan tun.Event, 1), done: make(chan struct{}),
	}
	d.events <- tun.EventUp
	return d
}

func (d *Device) File() *os.File           { return nil }
func (d *Device) MTU() (int, error)        { return d.mtu, nil }
func (d *Device) Name() (string, error)    { return d.name, nil }
func (d *Device) Events() <-chan tun.Event { return d.events }
func (d *Device) BatchSize() int           { return 1 }

// Inject supplies one host-mux packet to the embedded profile. It blocks until
// accepted or the device is closed; callers should arrange cancellation at the
// mux layer.
func (d *Device) Inject(packet []byte) error { return d.send(d.toEngine, packet) }

// TryInject supplies a packet without allowing a stalled embedded profile to
// block the shared host mux. Callers should drop and account for backpressure.
func (d *Device) TryInject(packet []byte) error {
	p := append([]byte(nil), packet...)
	select {
	case d.toEngine <- p:
		return nil
	case <-d.done:
		return errClosed
	default:
		return errQueueFull
	}
}

// Receive returns one packet emitted by the embedded profile.
func (d *Device) Receive() ([]byte, error) {
	select {
	case p := <-d.fromEngine:
		return p, nil
	case <-d.done:
		return nil, io.EOF
	}
}

func (d *Device) send(ch chan<- []byte, packet []byte) error {
	p := append([]byte(nil), packet...)
	select {
	case ch <- p:
		return nil
	case <-d.done:
		return errClosed
	}
}

// Read implements tun.Device. tsnet calls it to obtain packets to transmit.
func (d *Device) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	if len(bufs) == 0 || len(sizes) < len(bufs) {
		return 0, errors.New("invalid TUN read batch")
	}
	select {
	case p := <-d.toEngine:
		if len(bufs[0]) < offset+len(p) {
			return 0, io.ErrShortBuffer
		}
		copy(bufs[0][offset:], p)
		sizes[0] = len(p)
		return 1, nil
	case <-d.done:
		return 0, io.EOF
	}
}

// Write implements tun.Device. tsnet calls it for packets received from a
// tailnet peer. A full batch is accepted or the device reports closure.
func (d *Device) Write(bufs [][]byte, offset int) (int, error) {
	for i, b := range bufs {
		if offset > len(b) {
			return i, errors.New("invalid TUN write offset")
		}
		if err := d.send(d.fromEngine, b[offset:]); err != nil {
			return i, err
		}
	}
	return len(bufs), nil
}

func (d *Device) Close() error {
	d.closeOnce.Do(func() {
		close(d.done)
		close(d.events)
	})
	return nil
}

var _ tun.Device = (*Device)(nil)
