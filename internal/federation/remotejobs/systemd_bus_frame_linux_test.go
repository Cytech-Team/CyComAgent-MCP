//go:build linux

package remotejobs

import (
	"encoding/binary"
	"testing"
)

func makeTestDBusFixedHeader(order binary.ByteOrder, marker byte, messageType, flags byte, bodyLength, serial, fields uint32) []byte {
	b := make([]byte, systemdDBusFixedHeaderSize)
	b[0], b[1], b[2], b[3] = marker, messageType, flags, 1
	order.PutUint32(b[4:8], bodyLength)
	order.PutUint32(b[8:12], serial)
	order.PutUint32(b[12:16], fields)
	return b
}

func TestInspectSystemdDBusFrameHeaderBothByteOrders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		order  binary.ByteOrder
		marker byte
	}{
		{"little", binary.LittleEndian, 'l'}, {"big", binary.BigEndian, 'B'},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, err := inspectSystemdDBusFrameHeader(makeTestDBusFixedHeader(tc.order, tc.marker, 2, 0, 13, 9, 17))
			if err != nil {
				t.Fatal(err)
			}
			if h.serial != 9 || h.bodyLength != 13 || h.headerFields != 17 || h.bodyOffset != 40 || h.totalFrameSize != 53 {
				t.Fatalf("unexpected frame layout: %#v", h)
			}
		})
	}
}

func TestInspectSystemdDBusFrameHeaderRejectsInvalidAndOversizedHeaders(t *testing.T) {
	valid := makeTestDBusFixedHeader(binary.LittleEndian, 'l', 2, 0, 1, 1, 0)
	cases := map[string][]byte{
		"truncated":        valid[:15],
		"bad-byte-order":   append([]byte{'x'}, valid[1:]...),
		"bad-type":         append([]byte(nil), valid...),
		"reserved-flags":   append([]byte(nil), valid...),
		"bad-version":      append([]byte(nil), valid...),
		"zero-serial":      append([]byte(nil), valid...),
		"oversized-body":   append([]byte(nil), valid...),
		"oversized-fields": append([]byte(nil), valid...),
	}
	cases["bad-type"][1] = 0
	cases["reserved-flags"][2] = 0x80
	cases["bad-version"][3] = 2
	binary.LittleEndian.PutUint32(cases["zero-serial"][8:12], 0)
	binary.LittleEndian.PutUint32(cases["oversized-body"][4:8], systemdDBusMaxBodySize+1)
	binary.LittleEndian.PutUint32(cases["oversized-fields"][12:16], systemdDBusMaxHeaderFields+1)
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := inspectSystemdDBusFrameHeader(data); err == nil {
				t.Fatal("invalid frame header accepted")
			}
		})
	}
}

func TestInspectSystemdDBusFrameHeaderAlignmentAndBounds(t *testing.T) {
	for fields := uint32(0); fields < 16; fields++ {
		h, err := inspectSystemdDBusFrameHeader(makeTestDBusFixedHeader(binary.LittleEndian, 'l', 1, 0, 0, 1, fields))
		if err != nil {
			t.Fatal(err)
		}
		if h.bodyOffset%8 != 0 || h.totalFrameSize != h.bodyOffset {
			t.Fatalf("invalid alignment for fields=%d: %#v", fields, h)
		}
	}
}
