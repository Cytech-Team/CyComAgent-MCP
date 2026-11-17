//go:build linux

package remotejobs

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

func FuzzDecodeSystemdDBusFrameNeverPanics(f *testing.F) {
	builder := newDBusWireBuilder(binary.LittleEndian, 'l')
	f.Add(builder.signal(systemdDBusWellKnownName, ":1.42", ":1.43", 11))
	builder = newDBusWireBuilder(binary.BigEndian, 'B')
	f.Add(builder.signal(systemdDBusWellKnownName, ":1.42", ":1.43", 12))
	f.Add([]byte{})
	f.Add(make([]byte, systemdDBusFixedHeaderSize))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > systemdDBusMaxFrameSize {
			t.Skip()
		}
		_, _ = decodeSystemdDBusFrame(data, time.Unix(1, 0).UTC())
	})
}

func FuzzInspectSystemdDBusFrameHeaderNeverPanics(f *testing.F) {
	valid := makeTestDBusFixedHeader(binary.LittleEndian, 'l', 2, 0, 8, 1, 32)
	f.Add(valid)
	f.Add([]byte{})
	f.Add(make([]byte, systemdDBusFixedHeaderSize))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64 {
			t.Skip()
		}
		_, _ = inspectSystemdDBusFrameHeader(data)
	})
}

func FuzzReadBoundedSystemdDBusFrameNeverPanics(f *testing.F) {
	builder := newDBusWireBuilder(binary.LittleEndian, 'l')
	f.Add(builder.signal(systemdDBusWellKnownName, ":1.42", ":1.43", 301))
	builder = newDBusWireBuilder(binary.BigEndian, 'B')
	f.Add(builder.methodReturn(":1.42", 41, "/org/freedesktop/systemd1/job/13", 302))
	f.Add([]byte{})
	f.Add(make([]byte, systemdDBusFixedHeaderSize))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > systemdDBusMaxFrameSize {
			t.Skip()
		}
		_, _ = readBoundedSystemdDBusFrame(bytes.NewReader(data))
	})
}
