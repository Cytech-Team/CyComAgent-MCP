//go:build linux

package remotejobs

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

type systemdBusPreparedCall struct {
	Serial uint32
	Call   systemdBusBootstrapCall
	Frame  []byte
}

// encodeSystemdBusBootstrapCall encodes only the three fixed bootstrap method
// calls. It is not a general D-Bus encoder and accepts no caller-selected
// destination, path, interface, member, signature, or arbitrary body.
func encodeSystemdBusBootstrapCall(serial uint32, call systemdBusBootstrapCall) ([]byte, error) {
	if serial == 0 || call.Destination != systemdDBusDaemonName ||
		call.Path != systemdDBusObjectPath || call.Interface != systemdDBusInterface {
		return nil, fmt.Errorf("D-Bus bootstrap call identity is invalid")
	}
	switch call.Member {
	case "Hello":
		if call.Signature != "" || len(call.StringArgs) != 0 {
			return nil, fmt.Errorf("D-Bus Hello call must have an empty body")
		}
	case "AddMatch":
		if call.Signature != "s" || len(call.StringArgs) != 1 ||
			call.StringArgs[0] != systemdBusHandshakeAddMatchRule {
			return nil, fmt.Errorf("D-Bus AddMatch call arguments are not the fixed policy rule")
		}
	case "GetNameOwner":
		if call.Signature != "s" || len(call.StringArgs) != 1 ||
			call.StringArgs[0] != systemdDBusWellKnownName {
			return nil, fmt.Errorf("D-Bus GetNameOwner call argument is not the fixed systemd name")
		}
	default:
		return nil, fmt.Errorf("unsupported D-Bus bootstrap method %q", call.Member)
	}
	if !validDBusObjectPath(call.Path) || !validDBusInterfaceName(call.Interface) ||
		!validDBusMemberName(call.Member) || !validDBusBusName(call.Destination) {
		return nil, fmt.Errorf("D-Bus bootstrap call header contains an invalid name")
	}

	header := make([]byte, 0, 160)
	appendStringField := func(code byte, variant, value string) error {
		if len(value) > 255 || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
			return fmt.Errorf("D-Bus bootstrap header string is invalid")
		}
		header = alignDBusBytes(header, 8)
		header = append(header, code)
		header = appendDBusSignature(header, variant)
		header = alignDBusBytes(header, 4)
		header = appendDBusString(header, value)
		return nil
	}
	if err := appendStringField(1, "o", call.Path); err != nil {
		return nil, err
	}
	if err := appendStringField(2, "s", call.Interface); err != nil {
		return nil, err
	}
	if err := appendStringField(3, "s", call.Member); err != nil {
		return nil, err
	}
	if err := appendStringField(6, "s", call.Destination); err != nil {
		return nil, err
	}
	if call.Signature != "" {
		header = alignDBusBytes(header, 8)
		header = append(header, 8)
		header = appendDBusSignature(header, "g")
		header = appendDBusSignature(header, call.Signature)
	}

	body := make([]byte, 0, 128)
	if call.Signature == "s" {
		body = alignDBusBytes(body, 4)
		body = appendDBusString(body, call.StringArgs[0])
	}
	if len(header) > systemdDBusMaxHeaderFields || len(body) > systemdDBusMaxBodySize {
		return nil, fmt.Errorf("D-Bus bootstrap frame exceeds configured limits")
	}
	bodyOffset := (systemdDBusFixedHeaderSize + len(header) + 7) &^ 7
	total := bodyOffset + len(body)
	if total > systemdDBusMaxFrameSize || total > math.MaxUint32 {
		return nil, fmt.Errorf("D-Bus bootstrap frame size overflows policy")
	}
	frame := make([]byte, systemdDBusFixedHeaderSize, total)
	frame[0], frame[1], frame[2], frame[3] = 'l', 1, 0, 1
	binary.LittleEndian.PutUint32(frame[4:8], uint32(len(body)))
	binary.LittleEndian.PutUint32(frame[8:12], serial)
	binary.LittleEndian.PutUint32(frame[12:16], uint32(len(header)))
	frame = append(frame, header...)
	for len(frame) < bodyOffset {
		frame = append(frame, 0)
	}
	frame = append(frame, body...)
	return frame, nil
}

func alignDBusBytes(data []byte, alignment int) []byte {
	for len(data)%alignment != 0 {
		data = append(data, 0)
	}
	return data
}

func appendDBusSignature(dst []byte, signature string) []byte {
	dst = append(dst, byte(len(signature)))
	dst = append(dst, signature...)
	return append(dst, 0)
}

func appendDBusString(dst []byte, value string) []byte {
	var length [4]byte
	binary.LittleEndian.PutUint32(length[:], uint32(len(value)))
	dst = append(dst, length[:]...)
	dst = append(dst, value...)
	return append(dst, 0)
}
