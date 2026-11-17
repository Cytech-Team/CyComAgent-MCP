package remotejobs

import "bytes"

var containmentFailureMarkers = [][]byte{
	[]byte("Failed to connect to bus:"),
	[]byte("Failed to connect to user scope bus"),
	[]byte("Failed to create bus connection:"),
	[]byte("Unknown assignment:"),
	[]byte("Unknown property"),
	[]byte("Failed to set unit properties:"),
	[]byte("Failed to start transient scope unit:"),
	[]byte("Failed to start scope unit:"),
}

func isContainmentInfrastructureError(stderr []byte) bool {
	for _, marker := range containmentFailureMarkers {
		if bytes.Contains(stderr, marker) {
			return true
		}
	}
	return false
}
