package packettun

import (
	"bytes"
	"io"
	"testing"

	"github.com/tailscale/wireguard-go/tun"
)

func TestTryInjectDoesNotBlockOnFullQueue(t *testing.T) {
	d := New("profile-test", 1280, 1)
	defer d.Close()
	if err := d.TryInject([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := d.TryInject([]byte{2}); err != errQueueFull {
		t.Fatalf("TryInject = %v, want queue-full", err)
	}
}

func TestReadBufferRequirements(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		slabSize, packetCount int
	}{
		{"no metadata", 256, 0},
		{"no spacing", 2*tun.ReadPacketSpacing - 1, 1},
		{"packet too large", 2*tun.ReadPacketSpacing + 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := New("test", 1280, 1)
			defer d.Close()
			if err := d.Inject([]byte{1, 2, 3}); err != nil {
				t.Fatal(err)
			}
			if n, err := d.Read(make([]byte, tc.slabSize), make([]tun.ReadPacket, tc.packetCount)); n != 0 || err != tun.ErrTooManySegments {
				t.Fatalf("Read = %d, %v, want 0, ErrTooManySegments", n, err)
			}
		})
	}
}

func TestReadAfterClose(t *testing.T) {
	d := New("test", 1280, 1)
	d.Close()
	if n, err := d.Read(make([]byte, 256), make([]tun.ReadPacket, 1)); n != 0 || err != io.EOF {
		t.Fatalf("Read = %d, %v, want 0, EOF", n, err)
	}
}

func TestPacketOwnershipAndDirection(t *testing.T) {
	d := New("profile-test", 1280, 2)
	defer d.Close()

	in := []byte{1, 2, 3}
	if err := d.Inject(in); err != nil {
		t.Fatal(err)
	}
	in[0] = 9
	buf := make([]byte, 2*tun.ReadPacketSpacing+len(in))
	packets := make([]tun.ReadPacket, d.BatchSize())
	n, err := d.Read(buf, packets)
	if err != nil || n != 1 {
		t.Fatalf("Read = %d, %v", n, err)
	}
	if packets[0].Offset != tun.ReadPacketSpacing || packets[0].Size != len(in) {
		t.Fatalf("Read metadata = %+v", packets[0])
	}
	if got := buf[packets[0].Offset : packets[0].Offset+packets[0].Size]; !bytes.Equal(got, []byte{1, 2, 3}) {
		t.Fatalf("Read packet = %v", got)
	}

	out := []byte{0, 4, 5, 6}
	if n, err := d.Write([][]byte{out}, 1); err != nil || n != 1 {
		t.Fatalf("Write = %d, %v", n, err)
	}
	out[1] = 9
	got, err := d.Receive()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte{4, 5, 6}) {
		t.Fatalf("Receive packet = %v", got)
	}

	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Receive(); err != io.EOF {
		t.Fatalf("Receive after close = %v, want EOF", err)
	}
}
