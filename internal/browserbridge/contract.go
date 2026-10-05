package browserbridge

const (
	DefaultAddr      = "127.0.0.1:17373"
	APIURL           = "http://" + DefaultAddr + "/api"
	MaxRequestBytes  = 1 << 20
	MaxResponseBytes = 16 << 20
)
