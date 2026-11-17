//go:build linux

package remotejobs

import (
	"encoding/binary"
	"testing"
	"time"
)

type dbusWireBuilder struct {
	order        binary.ByteOrder
	marker       byte
	header, body []byte
}

func newDBusWireBuilder(order binary.ByteOrder, marker byte) *dbusWireBuilder {
	return &dbusWireBuilder{order: order, marker: marker}
}
func (b *dbusWireBuilder) alignHeader(n int) {
	for len(b.header)%n != 0 {
		b.header = append(b.header, 0)
	}
}
func (b *dbusWireBuilder) alignBody(n int) {
	for len(b.body)%n != 0 {
		b.body = append(b.body, 0)
	}
}
func (b *dbusWireBuilder) uint32(dst *[]byte, n uint32) {
	var v [4]byte
	b.order.PutUint32(v[:], n)
	*dst = append(*dst, v[:]...)
}
func (b *dbusWireBuilder) wireString(dst *[]byte, s string) {
	b.uint32(dst, uint32(len(s)))
	*dst = append(*dst, []byte(s)...)
	*dst = append(*dst, 0)
}
func (b *dbusWireBuilder) wireSignature(dst *[]byte, s string) {
	*dst = append(*dst, byte(len(s)))
	*dst = append(*dst, []byte(s)...)
	*dst = append(*dst, 0)
}
func (b *dbusWireBuilder) field(code byte, variant string, value string, number uint32) {
	b.alignHeader(8)
	b.header = append(b.header, code)
	b.wireSignature(&b.header, variant)
	switch variant {
	case "s", "o":
		b.alignHeader(4)
		b.wireString(&b.header, value)
	case "g":
		b.wireSignature(&b.header, value)
	case "u":
		b.alignHeader(4)
		b.uint32(&b.header, number)
	}
}
func (b *dbusWireBuilder) frame(messageType byte, serial uint32, signature string, bodyFn func()) []byte {
	b.body = nil
	bodyFn()
	bodyOffset := (16 + len(b.header) + 7) &^ 7
	frame := make([]byte, 16)
	frame[0], frame[1], frame[3] = b.marker, messageType, 1
	b.order.PutUint32(frame[4:8], uint32(len(b.body)))
	b.order.PutUint32(frame[8:12], serial)
	b.order.PutUint32(frame[12:16], uint32(len(b.header)))
	_ = signature
	frame = append(frame, b.header...)
	for len(frame) < bodyOffset {
		frame = append(frame, 0)
	}
	frame = append(frame, b.body...)
	return frame
}
func (b *dbusWireBuilder) signal(name, oldOwner, newOwner string, serial uint32) []byte {
	b.header = nil
	b.field(1, "o", "/org/freedesktop/DBus", 0)
	b.field(2, "s", "org.freedesktop.DBus", 0)
	b.field(3, "s", "NameOwnerChanged", 0)
	b.field(7, "s", systemdDBusDaemonName, 0)
	b.field(8, "g", "sss", 0)
	return b.frame(4, serial, "sss", func() {
		for _, s := range []string{name, oldOwner, newOwner} {
			b.alignBody(4)
			b.wireString(&b.body, s)
		}
	})
}
func (b *dbusWireBuilder) methodReturn(sender string, replySerial uint32, path string, serial uint32) []byte {
	b.header = nil
	b.field(5, "u", "", replySerial)
	b.field(7, "s", sender, 0)
	b.field(8, "g", "o", 0)
	return b.frame(2, serial, "o", func() { b.alignBody(4); b.wireString(&b.body, path) })
}

func TestDecodeSystemdDBusWireNameOwnerChanged(t *testing.T) {
	for _, tc := range []struct {
		name   string
		order  binary.ByteOrder
		marker byte
	}{{"little", binary.LittleEndian, 'l'}, {"big", binary.BigEndian, 'B'}} {
		t.Run(tc.name, func(t *testing.T) {
			b := newDBusWireBuilder(tc.order, tc.marker)
			frame := b.signal(systemdDBusWellKnownName, ":1.42", ":1.43", 11)
			m, err := decodeSystemdDBusFrame(frame, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeSystemdNameOwnerChanged(m); err != nil {
				t.Fatal(err)
			}
			if m.serial != 11 || m.signature != "sss" || m.bodyStrings[0] != systemdDBusWellKnownName {
				t.Fatalf("decoded message mismatch: %#v", m)
			}
		})
	}
}

func TestDecodeSystemdDBusWireMethodReply(t *testing.T) {
	b := newDBusWireBuilder(binary.LittleEndian, 'l')
	frame := b.methodReturn(":1.42", 41, "/org/freedesktop/systemd1/job/9", 12)
	m, err := decodeSystemdDBusFrame(frame, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	reply, err := decodeSystemdStartMethodReply(m)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Sender != ":1.42" || reply.ReplySerial != 41 || reply.JobObjectPath != "/org/freedesktop/systemd1/job/9" {
		t.Fatalf("reply mismatch: %#v", reply)
	}
}

func TestDecodeSystemdDBusWireRejectsMalformedFrames(t *testing.T) {
	b := newDBusWireBuilder(binary.LittleEndian, 'l')
	valid := b.signal(systemdDBusWellKnownName, ":1.42", ":1.43", 11)
	cases := map[string][]byte{"truncated": valid[:len(valid)-1], "trailing": append(append([]byte(nil), valid...), 0), "wrong-body-length": append([]byte(nil), valid...), "duplicate-field": append([]byte(nil), valid...), "bad-terminator": append([]byte(nil), valid...)}
	cases["wrong-body-length"][4]++
	// Duplicate the first header field verbatim by extending the header array.
	fieldStart := 16
	fieldEnd := 16 + int(binary.LittleEndian.Uint32(valid[12:16]))
	firstEnd := fieldStart + 1 + 1 + len("o") + 1
	for firstEnd%4 != 0 {
		firstEnd++
	}
	firstEnd += 4 + len("/org/freedesktop/DBus") + 1
	duplicate := append([]byte(nil), valid[:fieldEnd]...)
	duplicate = append(duplicate, valid[fieldStart:firstEnd]...)
	binary.LittleEndian.PutUint32(duplicate[12:16], uint32(fieldEnd-fieldStart+firstEnd-fieldStart))
	bodyOffset := (16 + int(binary.LittleEndian.Uint32(duplicate[12:16])) + 7) &^ 7
	duplicate = append(duplicate, make([]byte, bodyOffset-len(duplicate))...)
	duplicate = append(duplicate, valid[(16+int(binary.LittleEndian.Uint32(valid[12:16]))+7)&^7:]...)
	cases["duplicate-field"] = duplicate
	cases["bad-terminator"][len(cases["bad-terminator"])-1] = 1
	for name, frame := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeSystemdDBusFrame(frame, time.Now().UTC()); err == nil {
				t.Fatal("malformed D-Bus frame accepted")
			}
		})
	}
}

func TestDecodeSystemdDBusWireRejectsInvalidHeaderVariantsAndBodySignature(t *testing.T) {
	b := newDBusWireBuilder(binary.LittleEndian, 'l')
	frame := b.signal(systemdDBusWellKnownName, ":1.42", ":1.43", 11)
	badVariant := append([]byte(nil), frame...)
	badVariant[17] = 'u'
	if _, err := decodeSystemdDBusFrame(badVariant, time.Now().UTC()); err == nil {
		t.Fatal("wrong header variant accepted")
	}
	badSignature := append([]byte(nil), frame...)
	headerLen := int(binary.LittleEndian.Uint32(frame[12:16]))
	bodyOffset := (16 + headerLen + 7) &^ 7
	_ = bodyOffset
	// Locate the final signature header value and alter its content without changing the frame size.
	for i := 16; i+3 < len(frame); i++ {
		if frame[i] == 8 && frame[i+1] == 1 && frame[i+2] == 'g' && frame[i+3] == 0 {
			badSignature[i+5] = 'o'
			break
		}
	}
	if _, err := decodeSystemdDBusFrame(badSignature, time.Now().UTC()); err == nil {
		t.Fatal("unsupported body signature accepted")
	}
}

func TestDecodeSystemdDBusWireRequiresTypeSpecificHeaders(t *testing.T) {
	b := newDBusWireBuilder(binary.LittleEndian, 'l')
	cases := map[string][]byte{
		"signal-required-path-interface-member":      b.frame(4, 1, "", func() {}),
		"method-return-required-reply-serial":        b.frame(2, 2, "", func() {}),
		"error-required-error-name-and-reply-serial": b.frame(3, 3, "", func() {}),
		"method-call-required-path-and-member":       b.frame(1, 4, "", func() {}),
	}
	for name, frame := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeSystemdDBusFrame(frame, time.Now().UTC()); err == nil {
				t.Fatal("message missing required headers was accepted")
			}
		})
	}
}

func TestDecodeSystemdDBusWireRejectsNonzeroAlignmentPadding(t *testing.T) {
	b := newDBusWireBuilder(binary.LittleEndian, 'l')
	valid := b.signal(systemdDBusWellKnownName, ":1.42", ":1.43", 21)
	headerLength := int(binary.LittleEndian.Uint32(valid[12:16]))
	bodyOffset := (16 + headerLength + 7) &^ 7
	if bodyOffset == 16+headerLength {
		t.Fatalf("fixture has no header alignment padding: header=%d", headerLength)
	}
	bad := append([]byte(nil), valid...)
	bad[16+headerLength] = 1
	if _, err := decodeSystemdDBusFrame(bad, time.Now().UTC()); err == nil {
		t.Fatal("nonzero header alignment padding accepted")
	}
}
