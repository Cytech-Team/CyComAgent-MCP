package remotejobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

var ErrOutputLimit = errors.New("remote process exceeded output limit")

// LaunchResult contains bounded output only. ExitCode is -1 when execution
// outcome is ambiguous; callers must persist that as unknown and never replay.
type LaunchResult struct {
	ExitCode      int
	Stdout        []byte
	Stderr        []byte
	Duration      time.Duration
	OutputLimited bool
}

type cappedBuffer struct {
	mu         sync.Mutex
	buf        bytes.Buffer
	limit      int64
	used       int64
	overflow   bool
	onOverflow func()
	once       sync.Once
}

func (w *cappedBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	remaining := w.limit - w.used
	if remaining > 0 {
		n := int64(len(p))
		if n > remaining {
			n = remaining
		}
		_, _ = w.buf.Write(p[:n])
		w.used += n
	}
	if int64(len(p)) > remaining {
		w.overflow = true
		w.once.Do(w.onOverflow)
	}
	w.mu.Unlock()
	return len(p), nil // continue draining pipes so the child cannot block on a full pipe
}
func (w *cappedBuffer) snapshot() ([]byte, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buf.Bytes()...), w.overflow
}

// Launch executes a validated structured request directly with argv; it never
// invokes a shell and never inherits the parent environment. Linux requires a
// transient systemd control-group scope; jobs that cannot obtain that lifecycle
// boundary fail closed. This is not a sandbox against hostile same-UID code
// that can access the user's systemd manager and create a separate unit.
func Launch(ctx context.Context, policy ExecutionPolicy, callerNodeID string, callerTrusted bool, req ProcessRequest) (LaunchResult, error) {
	var result LaunchResult
	executable, workingDir, err := policy.validateAndResolve(callerNodeID, callerTrusted, req)
	if err != nil {
		return result, err
	}
	// Bind the executable to an open file description. On Linux the child
	// addresses this inherited descriptor through procfs, preventing pathname
	// replacement after validation from redirecting exec. The cwd is also bound
	// to its open directory descriptor before the child is started.
	if runtime.GOOS != "linux" {
		return result, fmt.Errorf("descriptor-bound remote execution is unsupported on %s", runtime.GOOS)
	}
	exeFile, err := os.Open(executable)
	if err != nil {
		return result, fmt.Errorf("open validated executable: %w", err)
	}
	defer exeFile.Close()
	exeInfo, err := exeFile.Stat()
	if err != nil || !exeInfo.Mode().IsRegular() || exeInfo.Mode()&0111 == 0 {
		return result, fmt.Errorf("opened executable is not a regular executable file")
	}
	pathInfo, err := os.Stat(executable)
	if err != nil || !os.SameFile(exeInfo, pathInfo) {
		return result, fmt.Errorf("executable changed while opening")
	}
	cwdFile, err := os.Open(workingDir)
	if err != nil {
		return result, fmt.Errorf("open validated working directory: %w", err)
	}
	defer cwdFile.Close()
	cwdInfo, err := cwdFile.Stat()
	if err != nil || !cwdInfo.IsDir() {
		return result, fmt.Errorf("opened working directory is not a directory")
	}
	cwdPathInfo, err := os.Stat(workingDir)
	if err != nil || !os.SameFile(cwdInfo, cwdPathInfo) {
		return result, fmt.Errorf("working directory changed while opening")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	started := time.Now()
	cmd, killContainedJob, err := newStructuredCommand(ctx, exeFile, cwdFile, req)
	if err != nil {
		return result, err
	}
	remaining := policy.MaxOutputBytes
	stdoutLimit := remaining / 2
	stderrLimit := remaining - stdoutLimit
	stdout := &cappedBuffer{limit: stdoutLimit, onOverflow: killContainedJob}
	stderr := &cappedBuffer{limit: stderrLimit, onOverflow: killContainedJob}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Start()
	if err != nil {
		return result, fmt.Errorf("start remote executable: %w", err)
	}
	err = cmd.Wait()
	result.Duration = time.Since(started)
	result.Stdout, _ = stdout.snapshot()
	result.Stderr, _ = stderr.snapshot()
	_, stdoutOverflow := stdout.snapshot()
	_, stderrOverflow := stderr.snapshot()
	result.OutputLimited = stdoutOverflow || stderrOverflow
	if ctx.Err() != nil {
		result.ExitCode = -1
		return result, fmt.Errorf("remote process outcome unknown: %w", ctx.Err())
	}
	if err != nil && isContainmentInfrastructureError(result.Stderr) {
		result.ExitCode = -1
		return result, fmt.Errorf("remote process containment failed: %s", strings.TrimSpace(string(result.Stderr)))
	}
	if result.OutputLimited {
		result.ExitCode = -1
		return result, ErrOutputLimit
	}
	if err == nil {
		result.ExitCode = 0
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	result.ExitCode = -1
	return result, fmt.Errorf("remote process outcome unknown: %w", err)
}
