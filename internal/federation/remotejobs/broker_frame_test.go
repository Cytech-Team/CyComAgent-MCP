package remotejobs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

type shortWriter struct{ max int }

func (w shortWriter) Write(p []byte) (int, error) {
	if len(p) > w.max {
		p = p[:w.max]
	}
	return len(p), nil
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

func TestBrokerFrameRoundTripAndMultipleFrames(t *testing.T) {
	var wire bytes.Buffer
	for _, payload := range [][]byte{[]byte("first"), []byte("second"), bytes.Repeat([]byte{'x'}, 4096)} {
		if err := WriteBrokerFrame(&wire, payload); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range [][]byte{[]byte("first"), []byte("second"), bytes.Repeat([]byte{'x'}, 4096)} {
		got, err := ReadBrokerFrame(&wire)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("frame mismatch: got %d bytes, want %d", len(got), len(want))
		}
	}
}

func TestBrokerFrameRejectsInvalidSizesBeforePayloadRead(t *testing.T) {
	for _, size := range []uint32{0, maxBrokerRequestBytes + 1, ^uint32(0)} {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], size)
		if _, err := ReadBrokerFrame(bytes.NewReader(header[:])); err == nil {
			t.Fatalf("accepted frame size %d", size)
		}
	}
}

func TestBrokerFrameRejectsTruncatedHeaderAndPayload(t *testing.T) {
	if _, err := ReadBrokerFrame(bytes.NewReader([]byte{0, 0})); err == nil {
		t.Fatal("accepted truncated header")
	}
	var wire bytes.Buffer
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], 5)
	wire.Write(header[:])
	wire.WriteString("ab")
	if _, err := ReadBrokerFrame(&wire); err == nil {
		t.Fatal("accepted truncated payload")
	}
}

func TestBrokerFrameWriteRejectsInvalidInputsAndShortWrites(t *testing.T) {
	if err := WriteBrokerFrame(nil, []byte("x")); err == nil {
		t.Fatal("accepted nil writer")
	}
	if err := WriteBrokerFrame(&bytes.Buffer{}, nil); err == nil {
		t.Fatal("accepted empty payload")
	}
	if err := WriteBrokerFrame(&bytes.Buffer{}, bytes.Repeat([]byte{'x'}, maxBrokerRequestBytes+1)); err == nil {
		t.Fatal("accepted oversized payload")
	}
	if err := WriteBrokerFrame(shortWriter{max: 1}, []byte("valid")); err != nil {
		t.Fatalf("short writes should be completed: %v", err)
	}
	if err := WriteBrokerFrame(zeroWriter{}, []byte("valid")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("zero writer error = %v, want ErrShortWrite", err)
	}
}
