package audit

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type Event struct {
	Time       time.Time `json:"time"`
	Tool       string    `json:"tool"`
	ArgsSHA256 string    `json:"args_sha256"`
	DurationMS int64     `json:"duration_ms"`
	OK         bool      `json:"ok"`
	Error      string    `json:"error,omitempty"`
	Reason     string    `json:"reason,omitempty"`
}

type ActiveCall struct {
	ID     string    `json:"id"`
	Time   time.Time `json:"time"`
	Tool   string    `json:"tool"`
	Reason string    `json:"reason,omitempty"`
}

type Logger struct {
	dir    string
	mu     sync.Mutex
	active map[string]ActiveCall
}

func New(stateDir string) (*Logger, error) {
	dir := filepath.Join(stateDir, "audit")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Logger{dir: dir, active: make(map[string]ActiveCall)}, nil
}

func callID(tool string, args json.RawMessage) string {
	sum := sha256.Sum256(append(append([]byte(tool), 0), args...))
	return hex.EncodeToString(sum[:])
}

func callReason(args json.RawMessage) string {
	var obj map[string]any
	_ = json.Unmarshal(args, &obj)
	reason, _ := obj["reason"].(string)
	return reason
}

func (l *Logger) BeforeCall(_ context.Context, tool string, args json.RawMessage) error {
	call := ActiveCall{ID: callID(tool, args), Time: time.Now().UTC(), Tool: tool, Reason: callReason(args)}
	l.mu.Lock()
	l.active[call.ID] = call
	l.mu.Unlock()
	return nil
}

func (l *Logger) AfterCall(_ context.Context, tool string, args json.RawMessage, d time.Duration, callErr error) {
	sum := sha256.Sum256(args)
	reason := callReason(args)
	l.mu.Lock()
	delete(l.active, callID(tool, args))
	l.mu.Unlock()
	ev := Event{
		Time:       time.Now().UTC(),
		Tool:       tool,
		ArgsSHA256: hex.EncodeToString(sum[:]),
		DurationMS: d.Milliseconds(),
		OK:         callErr == nil,
		Reason:     reason,
	}
	if callErr != nil {
		ev.Error = callErr.Error()
	}
	_ = l.append(ev)
}

func (l *Logger) append(ev Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	path := filepath.Join(l.dir, ev.Time.Format("2006-01-02")+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	return enc.Encode(ev)
}

func (l *Logger) Active() []ActiveCall {
	l.mu.Lock()
	out := make([]ActiveCall, 0, len(l.active))
	for _, call := range l.active {
		out = append(out, call)
	}
	l.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}

func (l *Logger) Tail(limit int) ([]Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".jsonl" {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	out := make([]Event, 0, limit)
	for _, name := range names {
		f, err := os.Open(filepath.Join(l.dir, name))
		if err != nil {
			continue
		}
		scan := bufio.NewScanner(f)
		scan.Buffer(make([]byte, 64<<10), 2<<20)
		fileEvents := make([]Event, 0)
		for scan.Scan() {
			var ev Event
			if json.Unmarshal(scan.Bytes(), &ev) == nil {
				fileEvents = append(fileEvents, ev)
			}
		}
		_ = f.Close()
		for i := len(fileEvents) - 1; i >= 0 && len(out) < limit; i-- {
			out = append(out, fileEvents[i])
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (l *Logger) Prune(olderThan time.Duration) (int, error) {
	if olderThan <= 0 {
		olderThan = 30 * 24 * time.Hour
	}
	cutoff := time.Now().Add(-olderThan)
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(l.dir, e.Name())); err != nil {
			return removed, fmt.Errorf("remove %s: %w", e.Name(), err)
		}
		removed++
	}
	return removed, nil
}
