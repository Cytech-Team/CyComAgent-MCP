package sessionbridge

// ExecRequest describes a synchronous command that must execute from inside
// the active graphical login session rather than from the CyComAgent service
// cgroup. The session bridge merges Env on top of the environment it inherited
// from the desktop session.
type ExecRequest struct {
	Command        string            `json:"command"`
	Stdin          string            `json:"stdin,omitempty"`
	Cwd            string            `json:"cwd,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Shell          string            `json:"shell,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	MaxOutputBytes int               `json:"max_output_bytes,omitempty"`
}

type ExecResult struct {
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	DurationMS int64  `json:"duration_ms"`
	TimedOut   bool   `json:"timed_out"`
	Truncated  bool   `json:"truncated"`
	PID        int    `json:"pid,omitempty"`
	Cgroup     string `json:"cgroup,omitempty"`
	Session    string `json:"session,omitempty"`
}

// SpawnRequest starts a detached command in the graphical login session. The
// caller owns LogFile and can monitor/signal the returned PID/PGID directly.
type SpawnRequest struct {
	Command string            `json:"command"`
	Cwd     string            `json:"cwd,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Shell   string            `json:"shell,omitempty"`
	LogFile string            `json:"log_file"`
}

type SpawnResult struct {
	PID     int    `json:"pid"`
	PGID    int    `json:"pgid,omitempty"`
	Cgroup  string `json:"cgroup,omitempty"`
	Session string `json:"session,omitempty"`
}

type Status struct {
	Available       bool              `json:"available"`
	PID             int               `json:"pid,omitempty"`
	UID             int               `json:"uid,omitempty"`
	Socket          string            `json:"socket,omitempty"`
	Cgroup          string            `json:"cgroup,omitempty"`
	Session         string            `json:"session,omitempty"`
	Desktop         string            `json:"desktop,omitempty"`
	SessionType     string            `json:"session_type,omitempty"`
	WaylandDisplay  string            `json:"wayland_display,omitempty"`
	Display         string            `json:"display,omitempty"`
	EnvironmentHint map[string]string `json:"environment,omitempty"`
}

type request struct {
	Action string        `json:"action"`
	Exec   *ExecRequest  `json:"exec,omitempty"`
	Spawn  *SpawnRequest `json:"spawn,omitempty"`
}

type response struct {
	OK     bool         `json:"ok"`
	Error  string       `json:"error,omitempty"`
	Exec   *ExecResult  `json:"exec,omitempty"`
	Spawn  *SpawnResult `json:"spawn,omitempty"`
	Status *Status      `json:"status,omitempty"`
}
