//go:build !linux

package remotejobs

import "fmt"

type BrokerSocketPolicy struct {
	SocketUID uint32
	ParentUID uint32
}

func ValidateBrokerSocketPath(string, BrokerSocketPolicy) error {
	return fmt.Errorf("broker socket path validation is supported only on Linux")
}

type BrokerSocketValidation struct{ Path string }

func (*BrokerSocketValidation) Close() error { return nil }
func ValidateBrokerSocketPathPinned(string, BrokerSocketPolicy) (*BrokerSocketValidation, error) {
	return nil, fmt.Errorf("broker socket path validation is supported only on Linux")
}
