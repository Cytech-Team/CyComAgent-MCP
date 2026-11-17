//go:build linux

package remotejobs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// SystemdUnitObservation is a read-only observation of one exact, journal-bound
// unit. It is not by itself authorization to remove an executable artifact.
type SystemdUnitObservation struct {
	UnitName     string
	LoadState    string
	ActiveState  string
	SubState     string
	InvocationID string
	ControlGroup string
	Status       string
	ObservedAt   time.Time
}

type systemdShowRunner func(context.Context, string, []string) ([]byte, error)

// SystemdUnitInspector queries only the exact unit name supplied from a
// validated LaunchIntentRecord. It never enumerates units and never mutates
// systemd state.
type SystemdUnitInspector struct {
	managerEnv []string
	run        systemdShowRunner
}

func NewUserSystemdUnitInspector() (*SystemdUnitInspector, error) {
	env := make([]string, 0, 2)
	for _, key := range []string{"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"} {
		value, ok := os.LookupEnv(key)
		if !ok || value == "" {
			return nil, fmt.Errorf("user systemd manager unavailable: %s is not configured", key)
		}
		env = append(env, key+"="+value)
	}
	binary, err := exec.LookPath("systemctl")
	if err != nil {
		return nil, fmt.Errorf("systemctl not found: %w", err)
	}
	return &SystemdUnitInspector{managerEnv: env, run: func(ctx context.Context, unit string, _ []string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, binary, "--user", "show", "--no-pager", "--property=Id,LoadState,ActiveState,SubState,InvocationID,ControlGroup", unit)
		cmd.Env = env
		return cmd.Output()
	}}, nil
}

func newSystemdUnitInspectorForTest(run systemdShowRunner) *SystemdUnitInspector {
	return &SystemdUnitInspector{run: run}
}

func (i *SystemdUnitInspector) Inspect(ctx context.Context, intent LaunchIntentRecord) (SystemdUnitObservation, error) {
	var out SystemdUnitObservation
	if i == nil || i.run == nil || ctx == nil {
		return out, fmt.Errorf("systemd inspector and context are required")
	}
	if err := strictLaunchIntentRecordMustBeValid(intent); err != nil {
		return out, err
	}
	out.UnitName = intent.UnitName
	props := []string{"Id", "LoadState", "ActiveState", "SubState", "InvocationID", "ControlGroup"}
	data, err := i.run(ctx, intent.UnitName, props)
	out.ObservedAt = time.Now().UTC()
	if err != nil {
		out.Status = "unknown"
		// systemctl show returns a non-zero exit for a unit that does not exist
		// on some systemd versions. Only the explicit LoadState=not-found
		// response is accepted as a definitive not-found observation.
		return out, fmt.Errorf("query exact systemd unit %s: %w", intent.UnitName, err)
	}
	values, err := parseSystemdShowProperties(data)
	if err != nil {
		return out, err
	}
	if values["Id"] != intent.UnitName {
		return out, fmt.Errorf("systemd returned a different unit identity")
	}
	out.LoadState, out.ActiveState, out.SubState = values["LoadState"], values["ActiveState"], values["SubState"]
	out.InvocationID, out.ControlGroup = values["InvocationID"], values["ControlGroup"]
	switch {
	case out.LoadState == "not-found":
		out.Status = "not_found"
	case out.LoadState != "loaded":
		out.Status = "unknown"
	case out.ActiveState == "active" || out.ActiveState == "activating" || out.ActiveState == "deactivating" || out.ActiveState == "reloading":
		if out.ControlGroup == "" {
			out.Status = "unknown"
		} else {
			out.Status = "active"
		}
	case out.ActiveState == "inactive":
		out.Status = "inactive"
	default:
		out.Status = "unknown"
	}
	return out, nil
}

func parseSystemdShowProperties(data []byte) (map[string]string, error) {
	values := make(map[string]string, 6)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("malformed systemd show property")
		}
		switch key {
		case "Id", "LoadState", "ActiveState", "SubState", "InvocationID", "ControlGroup":
			if _, exists := values[key]; exists {
				return nil, fmt.Errorf("duplicate systemd property %s", key)
			}
			values[key] = value
		default:
			return nil, fmt.Errorf("unexpected systemd property %s", key)
		}
	}
	for _, key := range []string{"Id", "LoadState", "ActiveState", "SubState", "InvocationID", "ControlGroup"} {
		if _, ok := values[key]; !ok {
			return nil, fmt.Errorf("missing systemd property %s", key)
		}
	}
	return values, nil
}

func strictLaunchIntentRecordMustBeValid(rec LaunchIntentRecord) error {
	// Reuse the strict serialized validator so in-memory records cannot bypass
	// the same identity, digest, state, and unit-name checks as disk records.
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	var validated LaunchIntentRecord
	return strictLaunchIntentRecord(data, &validated)
}
