//go:build linux

package remotejobs

import (
	"fmt"
	"strings"
	"time"
)

const (
	systemdDBusDaemonName = "org.freedesktop.DBus"
	systemdDBusObjectPath = "/org/freedesktop/DBus"
	systemdDBusInterface  = "org.freedesktop.DBus"
)

// systemdBusDecodedMessage is an internal representation populated only by
// decodeSystemdDBusFrame. Header and body fields must be copied from the wire
// frame, never synthesized from caller/request data. This package does not
// read a bus connection or authenticate a D-Bus transport.
type systemdBusDecodedMessage struct {
	messageType    string
	sender         string
	destination    string
	path           string
	interfaceName  string
	member         string
	signature      string
	serial         uint32
	replySerial    uint32
	bodyStrings    []string
	bodyObjectPath string
	errorName      string
	receivedAt     time.Time
}

type systemdBusOwnerChangedEvent struct {
	name, oldOwner, newOwner string
	receivedAt               time.Time
}

func decodeSystemdNameOwnerChanged(message systemdBusDecodedMessage) (systemdBusOwnerChangedEvent, error) {
	if message.messageType != "signal" || message.sender != systemdDBusDaemonName ||
		message.destination != "" || message.path != systemdDBusObjectPath ||
		message.interfaceName != systemdDBusInterface || message.member != "NameOwnerChanged" ||
		message.signature != "sss" || len(message.bodyStrings) != 3 || message.serial == 0 ||
		message.replySerial != 0 || message.bodyObjectPath != "" || message.errorName != "" {
		return systemdBusOwnerChangedEvent{}, fmt.Errorf("D-Bus message is not an exact NameOwnerChanged signal")
	}
	if message.receivedAt.IsZero() || !validDBusBusName(message.bodyStrings[0]) ||
		(message.bodyStrings[1] != "" && !validDBusUniqueName(message.bodyStrings[1])) ||
		(message.bodyStrings[2] != "" && !validDBusUniqueName(message.bodyStrings[2])) ||
		message.bodyStrings[1] == message.bodyStrings[2] {
		return systemdBusOwnerChangedEvent{}, fmt.Errorf("NameOwnerChanged signal body is invalid")
	}
	return systemdBusOwnerChangedEvent{
		name: message.bodyStrings[0], oldOwner: message.bodyStrings[1],
		newOwner: message.bodyStrings[2], receivedAt: message.receivedAt,
	}, nil
}

func decodeSystemdStartMethodReply(message systemdBusDecodedMessage) (systemdStartMethodReply, error) {
	if message.destination != "" || message.path != "" || message.interfaceName != "" ||
		message.member != "" || message.serial == 0 || message.replySerial == 0 ||
		message.receivedAt.IsZero() || !validDBusUniqueName(message.sender) {
		return systemdStartMethodReply{}, fmt.Errorf("D-Bus message is not a correlated systemd method reply")
	}
	reply := systemdStartMethodReply{ReplySerial: message.replySerial, Sender: message.sender, ReceivedAt: message.receivedAt}
	switch message.messageType {
	case "method_return":
		if message.signature != "o" || !validSystemdJobObjectPath(message.bodyObjectPath) || message.errorName != "" || len(message.bodyStrings) != 0 {
			return systemdStartMethodReply{}, fmt.Errorf("systemd method return body is invalid")
		}
		reply.JobObjectPath = message.bodyObjectPath
	case "error":
		if !validDBusInterfaceName(message.errorName) || message.bodyObjectPath != "" ||
			(message.signature == "" && len(message.bodyStrings) != 0) ||
			(message.signature == "s" && len(message.bodyStrings) != 1) ||
			(message.signature != "" && message.signature != "s") {
			return systemdStartMethodReply{}, fmt.Errorf("systemd method error header is invalid")
		}
		reply.ErrorName = message.errorName
	default:
		return systemdStartMethodReply{}, fmt.Errorf("unexpected D-Bus message type for method reply")
	}
	return reply, nil
}

func validDBusBusName(value string) bool {
	if strings.HasPrefix(value, ":") {
		return validDBusUniqueName(value)
	}
	if len(value) == 0 || len(value) > 255 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	parts := strings.Split(value, ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" || part[0] >= '0' && part[0] <= '9' {
			return false
		}
		for i := 0; i < len(part); i++ {
			c := part[i]
			if !isASCIIAlpha(c) && (c < '0' || c > '9') && c != '_' && c != '-' {
				return false
			}
		}
	}
	return true
}

func validDBusInterfaceName(value string) bool {
	if len(value) == 0 || len(value) > 255 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	parts := strings.Split(value, ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" || !isASCIIAlpha(part[0]) && part[0] != '_' {
			return false
		}
		for i := 1; i < len(part); i++ {
			c := part[i]
			if !isASCIIAlpha(c) && (c < '0' || c > '9') && c != '_' {
				return false
			}
		}
	}
	return true
}

func isASCIIAlpha(c byte) bool { return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }
