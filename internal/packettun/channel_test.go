package packettun

import (
	"bytes"
	"io"
	"testing"
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

func TestPacketOwnershipAndDirection(t *testing.T) {
	d := New("profile-test", 1280, 2)
	defer d.Close()

	in := []byte{1, 2, 3}
	if err := d.Inject(in); err != nil {
		t.Fatal(err)
	}
	in[0] = 9
	buf := make([]byte, 16)
	n, err := d.Read([][]byte{buf}, make([]int, 1), 4)
	if err != nil || n != 1 {
		t.Fatalf("Read = %d, %v", n, err)
	}
	if got := buf[4:7]; !bytes.Equal(got, []byte{1, 2, 3}) {
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
