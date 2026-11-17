//go:build linux

package remotejobs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
)

const cgroup2SuperMagic = 0x63677270
const maxCgroupControlFileBytes = 64 << 10

// CgroupObservation is a read-only snapshot of one systemd-reported cgroup.
// It is evidence for reconciliation, never standalone cleanup authorization.
type CgroupObservation struct {
	Path       string
	Present    bool
	Populated  bool
	ProcessIDs []int
	Device     uint64
	Inode      uint64
	Status     string
}

// VerifyCgroupAbsence opens the exact ControlGroup path relative to a pinned
// cgroup-v2 mount descriptor. It reads cgroup.events and cgroup.procs twice to
// detect obvious concurrent changes. The result is still only an observation:
// cgroup membership can change after the final read, so callers must not treat
// an empty snapshot as a lease or deletion authorization.
func VerifyCgroupAbsence(cgroupRoot *os.File, unit SystemdUnitObservation) (CgroupObservation, error) {
	var out CgroupObservation
	if cgroupRoot == nil {
		return out, fmt.Errorf("pinned cgroup v2 root is required")
	}
	if unit.Status != "inactive" && unit.Status != "not_found" {
		return out, fmt.Errorf("systemd unit must be inactive or not-found before cgroup inspection")
	}
	path := unit.ControlGroup
	if path == "" {
		return out, fmt.Errorf("systemd ControlGroup is unavailable")
	}
	if !strings.HasPrefix(path, "/") || path == "/" || strings.IndexByte(path, 0) >= 0 {
		return out, fmt.Errorf("invalid systemd ControlGroup path")
	}
	rootStat, err := cgroupRoot.Stat()
	if err != nil || !rootStat.IsDir() {
		return out, fmt.Errorf("cgroup root descriptor is not a directory")
	}
	var fs syscall.Statfs_t
	if err := syscall.Fstatfs(int(cgroupRoot.Fd()), &fs); err != nil {
		return out, err
	}
	if uint64(fs.Type) != cgroup2SuperMagic {
		return out, fmt.Errorf("descriptor is not a cgroup v2 filesystem")
	}
	relative := strings.TrimPrefix(path, "/")
	cg, err := OpenBeneath(cgroupRoot, relative, uint64(openPath|syscall.O_DIRECTORY), 0)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			out.Path, out.Status = path, "missing"
			return out, nil
		}
		return out, fmt.Errorf("open systemd cgroup path: %w", err)
	}
	defer cg.Close()
	info, err := cg.Stat()
	if err != nil || !info.IsDir() {
		return out, fmt.Errorf("opened cgroup is not a directory")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return out, fmt.Errorf("cgroup stat identity unavailable")
	}
	out.Path, out.Present, out.Device, out.Inode = path, true, uint64(st.Dev), st.Ino
	firstEvents, firstProcs, err := readCgroupSnapshot(cg)
	if err != nil {
		return out, err
	}
	secondEvents, secondProcs, err := readCgroupSnapshot(cg)
	if err != nil {
		return out, err
	}
	if firstEvents != secondEvents || !samePIDSet(firstProcs, secondProcs) {
		out.Status = "changing"
		return out, nil
	}
	out.Populated, out.ProcessIDs = secondEvents, secondProcs
	if out.Populated || len(out.ProcessIDs) != 0 {
		out.Status = "populated"
	} else {
		out.Status = "empty_observed"
	}
	return out, nil
}

func readCgroupSnapshot(dir *os.File) (bool, []int, error) {
	events, err := readCgroupControlFile(dir, "cgroup.events")
	if err != nil {
		return false, nil, err
	}
	procs, err := readCgroupControlFile(dir, "cgroup.procs")
	if err != nil {
		return false, nil, err
	}
	populated, found := false, false
	for _, line := range strings.Split(strings.TrimSpace(string(events)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "populated" && (fields[1] == "0" || fields[1] == "1") {
			if found {
				return false, nil, fmt.Errorf("duplicate populated field in cgroup.events")
			}
			found, populated = true, fields[1] == "1"
		}
	}
	if !found {
		return false, nil, fmt.Errorf("cgroup.events lacks populated field")
	}
	var pids []int
	for _, line := range strings.Fields(string(procs)) {
		pid, err := strconv.Atoi(line)
		if err != nil || pid <= 0 {
			return false, nil, fmt.Errorf("invalid PID in cgroup.procs")
		}
		pids = append(pids, pid)
	}
	return populated, pids, nil
}

func readCgroupControlFile(dir *os.File, name string) ([]byte, error) {
	f, err := OpenBeneath(dir, name, uint64(syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK), 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxCgroupControlFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxCgroupControlFileBytes {
		return nil, fmt.Errorf("cgroup control file exceeds size limit")
	}
	return data, nil
}

func samePIDSet(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[int]int, len(a))
	for _, pid := range a {
		counts[pid]++
	}
	for _, pid := range b {
		counts[pid]--
		if counts[pid] < 0 {
			return false
		}
	}
	return true
}
