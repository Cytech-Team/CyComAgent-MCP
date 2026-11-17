//go:build linux

package remotejobs

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// decodeSystemdDBusFrame decodes the narrowly supported D-Bus wire messages
// used by the systemd start protocol. It accepts only exact frame-sized input,
// rejects duplicate/unknown header fields, and never allocates from an
// unvalidated wire length. Authentication and socket I/O remain out of scope.
func decodeSystemdDBusFrame(frame []byte, receivedAt time.Time) (systemdBusDecodedMessage, error) {
	if receivedAt.IsZero() || len(frame) < systemdDBusFixedHeaderSize {
		return systemdBusDecodedMessage{}, fmt.Errorf("D-Bus frame or receive timestamp is invalid")
	}
	h, err := inspectSystemdDBusFrameHeader(frame[:systemdDBusFixedHeaderSize])
	if err != nil {
		return systemdBusDecodedMessage{}, err
	}
	if uint64(len(frame)) != uint64(h.totalFrameSize) {
		return systemdBusDecodedMessage{}, fmt.Errorf("D-Bus frame length does not match fixed header")
	}
	fieldsEnd := systemdDBusFixedHeaderSize + int(h.headerFields)
	fields := frame[systemdDBusFixedHeaderSize:fieldsEnd]
	if !allZeroDBusPadding(frame[fieldsEnd:int(h.bodyOffset)]) {
		return systemdBusDecodedMessage{}, fmt.Errorf("D-Bus header-to-body alignment padding is nonzero")
	}
	values, err := parseSystemdDBusHeaderFields(fields, h.byteOrder)
	if err != nil {
		return systemdBusDecodedMessage{}, err
	}
	body := frame[int(h.bodyOffset):]
	if err := validateSystemdDBusHeader(h.messageType, values, len(body)); err != nil {
		return systemdBusDecodedMessage{}, err
	}
	message := systemdBusDecodedMessage{serial: h.serial, receivedAt: receivedAt,
		sender: values[7].s, destination: values[6].s, path: values[1].s,
		interfaceName: values[2].s, member: values[3].s, errorName: values[4].s,
		replySerial: values[5].u, signature: values[8].s}
	switch h.messageType {
	case 1:
		message.messageType = "method_call"
	case 2:
		message.messageType = "method_return"
	case 3:
		message.messageType = "error"
	case 4:
		message.messageType = "signal"
	}
	if err := decodeSystemdDBusBody(&message, body, h.byteOrder); err != nil {
		return systemdBusDecodedMessage{}, err
	}
	return message, nil
}

type systemdDBusHeaderValue struct {
	s string
	u uint32
}

func parseSystemdDBusHeaderFields(data []byte, order binary.ByteOrder) (map[byte]systemdDBusHeaderValue, error) {
	values := make(map[byte]systemdDBusHeaderValue, 8)
	for offset := 0; offset < len(data); {
		var err error
		offset, err = alignSystemdDBusChecked(data, offset, 8)
		if err != nil {
			return nil, err
		}
		if offset >= len(data) {
			return nil, fmt.Errorf("D-Bus header array ends with alignment padding")
		}
		code := data[offset]
		offset++
		if code < 1 || code > 8 {
			return nil, fmt.Errorf("unsupported D-Bus header field code %d", code)
		}
		if _, exists := values[code]; exists {
			return nil, fmt.Errorf("duplicate D-Bus header field code %d", code)
		}
		if offset >= len(data) {
			return nil, fmt.Errorf("D-Bus header variant is truncated")
		}
		sig, next, err := readDBusSignature(data, offset)
		if err != nil {
			return nil, err
		}
		offset = next
		expected := map[byte]string{1: "o", 2: "s", 3: "s", 4: "s", 5: "u", 6: "s", 7: "s", 8: "g"}[code]
		if sig != expected {
			return nil, fmt.Errorf("D-Bus header field %d has variant type %q, expected %q", code, sig, expected)
		}
		var value systemdDBusHeaderValue
		switch sig {
		case "s", "o":
			offset, err = alignSystemdDBusChecked(data, offset, 4)
			if err != nil {
				return nil, err
			}
			value.s, offset, err = readDBusString(data, offset, order)
			if err == nil && sig == "o" && !validDBusObjectPath(value.s) {
				err = fmt.Errorf("D-Bus object path header is invalid")
			}
		case "u":
			offset, err = alignSystemdDBusChecked(data, offset, 4)
			if err != nil {
				return nil, err
			}
			if offset+4 > len(data) {
				err = fmt.Errorf("D-Bus uint32 header is truncated")
			} else {
				value.u = order.Uint32(data[offset : offset+4])
				offset += 4
			}
		case "g":
			value.s, offset, err = readDBusSignature(data, offset)
		}
		if err != nil {
			return nil, err
		}
		values[code] = value
	}
	return values, nil
}

func validateSystemdDBusHeader(messageType byte, values map[byte]systemdDBusHeaderValue, bodyLength int) error {
	has := func(code byte) bool { _, ok := values[code]; return ok }
	require := func(codes ...byte) error {
		for _, code := range codes {
			if !has(code) {
				return fmt.Errorf("D-Bus message type %d is missing required header field %d", messageType, code)
			}
		}
		return nil
	}
	switch messageType {
	case 1: // method_call: PATH and MEMBER are mandatory.
		if err := require(1, 3); err != nil {
			return err
		}
	case 2: // method_return: REPLY_SERIAL is mandatory.
		if err := require(5); err != nil {
			return err
		}
	case 3: // error: ERROR_NAME and REPLY_SERIAL are mandatory.
		if err := require(4, 5); err != nil {
			return err
		}
	case 4: // signal: PATH, INTERFACE and MEMBER are mandatory.
		if err := require(1, 2, 3); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported D-Bus message type %d", messageType)
	}
	if bodyLength > 0 && !has(8) {
		return fmt.Errorf("D-Bus nonempty body is missing its signature header")
	}
	if path := values[1].s; has(1) && !validDBusObjectPath(path) {
		return fmt.Errorf("D-Bus PATH header is invalid")
	}
	if iface := values[2].s; has(2) && !validDBusInterfaceName(iface) {
		return fmt.Errorf("D-Bus INTERFACE header is invalid")
	}
	if member := values[3].s; has(3) && !validDBusMemberName(member) {
		return fmt.Errorf("D-Bus MEMBER header is invalid")
	}
	if name := values[4].s; has(4) && !validDBusInterfaceName(name) {
		return fmt.Errorf("D-Bus ERROR_NAME header is invalid")
	}
	if destination := values[6].s; has(6) && !validDBusBusName(destination) {
		return fmt.Errorf("D-Bus DESTINATION header is invalid")
	}
	if sender := values[7].s; has(7) && !validDBusBusName(sender) {
		return fmt.Errorf("D-Bus SENDER header is invalid")
	}
	if signature := values[8].s; has(8) && signature != "" && signature != "o" && signature != "s" && signature != "sss" {
		return fmt.Errorf("D-Bus SIGNATURE header is outside the supported profile")
	}
	return nil
}

func validDBusMemberName(value string) bool {
	if len(value) == 0 || len(value) > 255 || (!isASCIIAlpha(value[0]) && value[0] != '_') {
		return false
	}
	for i := 1; i < len(value); i++ {
		c := value[i]
		if !isASCIIAlpha(c) && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

func decodeSystemdDBusBody(message *systemdBusDecodedMessage, body []byte, order binary.ByteOrder) error {
	if message == nil {
		return fmt.Errorf("D-Bus decoded message target is required")
	}
	if len(body) == 0 && message.signature == "" {
		return nil
	}
	offset := 0
	readString := func() (string, error) {
		var err error
		offset, err = alignSystemdDBusChecked(body, offset, 4)
		if err != nil {
			return "", err
		}
		value, next, err := readDBusString(body, offset, order)
		offset = next
		return value, err
	}
	switch message.signature {
	case "":
		if len(body) != 0 {
			return fmt.Errorf("D-Bus empty signature has a nonempty body")
		}
	case "o":
		var err error
		offset, err = alignSystemdDBusChecked(body, offset, 4)
		if err != nil {
			return err
		}
		value, next, err := readDBusString(body, offset, order)
		if err != nil {
			return err
		}
		offset = next
		if !validDBusObjectPath(value) {
			return fmt.Errorf("D-Bus body object path is invalid")
		}
		message.bodyObjectPath = value
	case "s":
		value, err := readString()
		if err != nil {
			return err
		}
		message.bodyStrings = []string{value}
	case "sss":
		message.bodyStrings = make([]string, 0, 3)
		for i := 0; i < 3; i++ {
			value, err := readString()
			if err != nil {
				return err
			}
			message.bodyStrings = append(message.bodyStrings, value)
		}
	default:
		return fmt.Errorf("D-Bus body signature %q is outside the supported transport profile", message.signature)
	}
	if offset != len(body) {
		return fmt.Errorf("D-Bus body contains trailing or unparsed bytes")
	}
	return nil
}

func readDBusString(data []byte, offset int, order binary.ByteOrder) (string, int, error) {
	if offset < 0 || offset+4 > len(data) {
		return "", offset, fmt.Errorf("D-Bus string length is truncated")
	}
	n := uint64(order.Uint32(data[offset : offset+4]))
	offset += 4
	if n > uint64(len(data)-offset) || n+1 > uint64(len(data)-offset) {
		return "", offset, fmt.Errorf("D-Bus string exceeds enclosing frame")
	}
	end := offset + int(n)
	if data[end] != 0 || bytes.IndexByte(data[offset:end], 0) >= 0 || !utf8.Valid(data[offset:end]) {
		return "", offset, fmt.Errorf("D-Bus string encoding or terminator is invalid")
	}
	return string(data[offset:end]), end + 1, nil
}

func readDBusSignature(data []byte, offset int) (string, int, error) {
	if offset >= len(data) {
		return "", offset, fmt.Errorf("D-Bus signature length is truncated")
	}
	n := int(data[offset])
	offset++
	if n > len(data)-offset-1 || offset+n >= len(data) {
		return "", offset, fmt.Errorf("D-Bus signature exceeds enclosing frame")
	}
	end := offset + n
	if data[end] != 0 || bytes.IndexByte(data[offset:end], 0) >= 0 || !utf8.Valid(data[offset:end]) {
		return "", offset, fmt.Errorf("D-Bus signature encoding or terminator is invalid")
	}
	return string(data[offset:end]), end + 1, nil
}

func alignSystemdDBus(offset, alignment int) int { return (offset + alignment - 1) &^ (alignment - 1) }

func alignSystemdDBusChecked(data []byte, offset, alignment int) (int, error) {
	if offset < 0 || offset > len(data) || alignment <= 0 || alignment&(alignment-1) != 0 {
		return offset, fmt.Errorf("D-Bus alignment request is invalid")
	}
	aligned := alignSystemdDBus(offset, alignment)
	if aligned > len(data) {
		return offset, fmt.Errorf("D-Bus alignment padding exceeds enclosing section")
	}
	if !allZeroDBusPadding(data[offset:aligned]) {
		return offset, fmt.Errorf("D-Bus alignment padding is nonzero")
	}
	return aligned, nil
}

func allZeroDBusPadding(data []byte) bool {
	for _, value := range data {
		if value != 0 {
			return false
		}
	}
	return true
}

func validDBusObjectPath(value string) bool {
	if value == "/" {
		return true
	}
	if len(value) < 2 || value[0] != '/' || strings.HasSuffix(value, "/") {
		return false
	}
	for _, part := range strings.Split(value[1:], "/") {
		if part == "" {
			return false
		}
		for i := 0; i < len(part); i++ {
			c := part[i]
			if !isASCIIAlpha(c) && (c < '0' || c > '9') && c != '_' {
				return false
			}
		}
	}
	return true
}
