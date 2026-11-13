//go:build linux

package hosttun

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/tailscale/wireguard-go/tun"
)

type batchTUN struct {
	tun.Device
	read func([]byte, []tun.ReadPacket) (int, error)
}

func (*batchTUN) BatchSize() int { return 2 }
func (d *batchTUN) Read(slab []byte, packets []tun.ReadPacket) (int, error) {
	return d.read(slab, packets)
}

func TestReadPacketDrainsBatchBeforeError(t *testing.T) {
	calls := 0
	d := &Device{Tun: &batchTUN{read: func(slab []byte, packets []tun.ReadPacket) (int, error) {
		calls++
		for i := range packets {
			offset := tun.ReadPacketSpacing + i*(tun.ReadPacketSpacing+3)
			copy(slab[offset:], []byte{byte(i + 1), 2, 3})
			packets[i] = tun.ReadPacket{Offset: offset, Size: 3}
		}
		return 2, io.EOF
	}}}
	buf := make([]byte, 128)
	for i := 0; i < 2; i++ {
		got, err := d.ReadPacket(buf)
		if err != nil || !bytes.Equal(got, []byte{byte(i + 1), 2, 3}) {
			t.Fatalf("packet %d = %v, %v", i, got, err)
		}
		// Returned packets own their bytes, independent of the caller buffer.
		clear(buf)
	}
	if _, err := d.ReadPacket(buf); !errors.Is(err, io.EOF) {
		t.Fatalf("ReadPacket after batch = %v, want EOF", err)
	}
	if calls != 1 {
		t.Fatalf("TUN reads = %d, want 1", calls)
	}
}

func TestReadPacketRejectsInvalidMetadata(t *testing.T) {
	for _, packet := range []tun.ReadPacket{
		{Offset: -1, Size: 1},
		{Offset: 0, Size: 1},
		{Offset: tun.ReadPacketSpacing, Size: 0},
		{Offset: tun.ReadPacketSpacing, Size: 1 << 30},
		{Offset: 1 << 30, Size: 1},
	} {
		t.Run("invalid", func(t *testing.T) {
			d := &Device{Tun: &batchTUN{read: func(_ []byte, packets []tun.ReadPacket) (int, error) {
				packets[0] = packet
				return 1, nil
			}}}
			if _, err := d.ReadPacket(make([]byte, 128)); err == nil {
				t.Fatalf("accepted metadata %+v", packet)
			}
		})
	}
}
