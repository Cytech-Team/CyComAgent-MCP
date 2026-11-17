//go:build linux

package remotejobs

import (
	"encoding/binary"
	"fmt"
)

const (
	systemdDBusFixedHeaderSize = 16
	systemdDBusMaxHeaderFields = 16 * 1024
	systemdDBusMaxBodySize     = 8 * 1024
	systemdDBusMaxFrameSize    = systemdDBusFixedHeaderSize + systemdDBusMaxHeaderFields + 7 + systemdDBusMaxBodySize
)

type systemdDBusFrameHeader struct {
	byteOrder      binary.ByteOrder
	messageType    byte
	flags          byte
	bodyLength     uint32
	serial         uint32
	headerFields   uint32
	bodyOffset     uint32
	totalFrameSize uint32
}

// inspectSystemdDBusFrameHeader validates the fixed 16-byte D-Bus header
// without allocating memory. The caller must read exactly TotalFrameSize bytes
// under a separate bounded transport deadline before parsing variable fields.
// This deliberately does not parse header-field variants or the message body.
func inspectSystemdDBusFrameHeader(fixed []byte) (systemdDBusFrameHeader, error) {
	if len(fixed) < systemdDBusFixedHeaderSize {
		return systemdDBusFrameHeader{}, fmt.Errorf("D-Bus fixed header is truncated")
	}
	var order binary.ByteOrder
	switch fixed[0] {
	case 'l':
		order = binary.LittleEndian
	case 'B':
		order = binary.BigEndian
	default:
		return systemdDBusFrameHeader{}, fmt.Errorf("D-Bus byte order marker is invalid")
	}
	messageType, flags, version := fixed[1], fixed[2], fixed[3]
	if messageType < 1 || messageType > 4 || flags&^byte(0x07) != 0 || version != 1 {
		return systemdDBusFrameHeader{}, fmt.Errorf("D-Bus message type, flags, or protocol version is invalid")
	}
	bodyLength := order.Uint32(fixed[4:8])
	serial := order.Uint32(fixed[8:12])
	headerFields := order.Uint32(fixed[12:16])
	if serial == 0 || headerFields > systemdDBusMaxHeaderFields || bodyLength > systemdDBusMaxBodySize {
		return systemdDBusFrameHeader{}, fmt.Errorf("D-Bus serial or frame section exceeds policy limits")
	}
	bodyOffset64 := uint64(systemdDBusFixedHeaderSize) + uint64(headerFields)
	bodyOffset64 = (bodyOffset64 + 7) &^ uint64(7)
	total64 := bodyOffset64 + uint64(bodyLength)
	if bodyOffset64 > uint64(systemdDBusMaxFrameSize) || total64 > uint64(systemdDBusMaxFrameSize) || total64 > uint64(^uint32(0)) {
		return systemdDBusFrameHeader{}, fmt.Errorf("D-Bus frame size overflows or exceeds policy limit")
	}
	return systemdDBusFrameHeader{
		byteOrder: order, messageType: messageType, flags: flags,
		bodyLength: bodyLength, serial: serial, headerFields: headerFields,
		bodyOffset: uint32(bodyOffset64), totalFrameSize: uint32(total64),
	}, nil
}
