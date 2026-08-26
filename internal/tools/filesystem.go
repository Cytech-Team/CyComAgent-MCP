package tools

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
)

const maxReadBytes = 10 << 20

func registerFilesystem(r *registry.Registry) {
	must(r.Add(registry.Tool{
		Name: "fs_list", Description: "List a local directory with metadata.",
		InputSchema: registry.ObjectSchema(map[string]any{
			"path":   registry.String("directory to list; defaults to current directory"),
			"hidden": registry.Boolean("include dotfiles"),
		}, nil), Handler: fsList, Source: "core",
	}))
	must(r.Add(registry.Tool{
		Name: "fs_read", Description: "Read a local file by byte range. Text is returned as UTF-8; binary data is returned as base64.",
		InputSchema: registry.ObjectSchema(map[string]any{
			"path":      registry.String("file to read"),
			"offset":    registry.Integer("byte offset"),
			"max_bytes": registry.Integer("maximum bytes to return, capped at 10 MiB"),
		}, []string{"path"}), Handler: fsRead, Source: "core",
		Annotations: map[string]any{"readOnlyHint": true},
	}))
	must(r.Add(registry.Tool{
		Name: "fs_write", Description: "Create, replace, or append a local file. Content can be UTF-8 text or base64.",
		InputSchema: registry.ObjectSchema(map[string]any{
			"path":     registry.String("file path"),
			"content":  registry.String("file content"),
			"encoding": map[string]any{"type": "string", "enum": []string{"utf-8", "base64"}, "description": "content encoding; default utf-8"},
			"append":   registry.Boolean("append instead of replacing"),
			"mode":     registry.Integer("unix mode bits, e.g. 420 for 0644"),
		}, []string{"path", "content"}), Handler: fsWrite, Source: "core",
	}))
	must(r.Add(registry.Tool{
		Name: "fs_patch", Description: "Atomically apply ordered exact-text replacements to a file.",
		InputSchema: registry.ObjectSchema(map[string]any{
			"path": registry.String("file path"),
			"edits": map[string]any{
				"type": "array", "minItems": 1,
				"items": registry.ObjectSchema(map[string]any{
					"old":   registry.String("exact text to find"),
					"new":   registry.String("replacement text"),
					"count": registry.Integer("maximum replacements; 0 means all"),
				}, []string{"old", "new"}),
			},
		}, []string{"path", "edits"}), Handler: fsPatch, Source: "core",
	}))
	must(r.Add(registry.Tool{Name: "fs_move", Description: "Move or rename a file or directory.", InputSchema: registry.ObjectSchema(map[string]any{
		"source": registry.String("source path"), "dest": registry.String("destination path"),
	}, []string{"source", "dest"}), Handler: fsMove, Source: "core"}))
	must(r.Add(registry.Tool{Name: "fs_remove", Description: "Remove a file or directory.", InputSchema: registry.ObjectSchema(map[string]any{
		"path": registry.String("path to remove"), "recursive": registry.Boolean("allow recursive directory removal"),
	}, []string{"path"}), Handler: fsRemove, Source: "core", Annotations: map[string]any{"destructiveHint": true}}))
	must(r.Add(registry.Tool{Name: "fs_search", Description: "Recursively search text files with a regular expression.", InputSchema: registry.ObjectSchema(map[string]any{
		"root":           registry.String("directory tree to search"),
		"pattern":        registry.String("regular expression matched against lines"),
		"name":           registry.String("optional substring required in filename"),
		"max_results":    registry.Integer("maximum result count"),
		"max_file_bytes": registry.Integer("skip files larger than this; default 4 MiB"),
	}, []string{"root", "pattern"}), Handler: fsSearch, Source: "core", Annotations: map[string]any{"readOnlyHint": true}}))
	must(r.Add(registry.Tool{Name: "fs_stat", Description: "Inspect a path without changing it.", InputSchema: registry.ObjectSchema(map[string]any{
		"path": registry.String("path to inspect"),
	}, []string{"path"}), Handler: fsStat, Source: "core", Annotations: map[string]any{"readOnlyHint": true}}))
}

type fsListInput struct {
	Path   string `json:"path"`
	Hidden bool   `json:"hidden"`
}
type fsReadInput struct {
	Path     string `json:"path"`
	Offset   int64  `json:"offset"`
	MaxBytes int64  `json:"max_bytes"`
}
type fsWriteInput struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
	Append   bool   `json:"append"`
	Mode     uint32 `json:"mode"`
}
type patchEdit struct {
	Old   string `json:"old"`
	New   string `json:"new"`
	Count int    `json:"count"`
}
type fsPatchInput struct {
	Path  string      `json:"path"`
	Edits []patchEdit `json:"edits"`
}
type fsMoveInput struct {
	Source string `json:"source"`
	Dest   string `json:"dest"`
}
type fsRemoveInput struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
}
type fsSearchInput struct {
	Root         string `json:"root"`
	Pattern      string `json:"pattern"`
	Name         string `json:"name"`
	MaxResults   int    `json:"max_results"`
	MaxFileBytes int64  `json:"max_file_bytes"`
}
type fsPathInput struct {
	Path string `json:"path"`
}

func fsList(ctx context.Context, raw json.RawMessage) (any, error) {
	var in fsListInput
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if in.Path == "" {
		in.Path = "."
	}
	entries, err := os.ReadDir(in.Path)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		if !in.Hidden && strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, map[string]any{
			"name": e.Name(), "path": filepath.Join(in.Path, e.Name()), "type": fileType(info),
			"size": info.Size(), "mode": info.Mode().String(), "mod_time": info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["name"].(string) < out[j]["name"].(string) })
	return map[string]any{"path": in.Path, "entries": out}, nil
}

func fsRead(_ context.Context, raw json.RawMessage) (any, error) {
	var in fsReadInput
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if in.Path == "" {
		return nil, fmt.Errorf("path is required")
	}
	limit := in.MaxBytes
	if limit <= 0 || limit > maxReadBytes {
		limit = maxReadBytes
	}
	f, err := os.Open(in.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if in.Offset > 0 {
		if _, err := f.Seek(in.Offset, io.SeekStart); err != nil {
			return nil, err
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	truncated := int64(len(data)) > limit
	if truncated {
		data = data[:limit]
	}
	enc, content := encodeFileData(data)
	return map[string]any{"path": in.Path, "encoding": enc, "content": content, "bytes_read": len(data), "truncated": truncated}, nil
}

func fsWrite(_ context.Context, raw json.RawMessage) (any, error) {
	var in fsWriteInput
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if in.Path == "" {
		return nil, fmt.Errorf("path is required")
	}
	data := []byte(in.Content)
	if in.Encoding == "base64" {
		b, err := base64.StdEncoding.DecodeString(in.Content)
		if err != nil {
			return nil, fmt.Errorf("decode base64: %w", err)
		}
		data = b
	}
	if in.Encoding != "" && in.Encoding != "utf-8" && in.Encoding != "base64" {
		return nil, fmt.Errorf("unsupported encoding %q", in.Encoding)
	}
	dir := filepath.Dir(in.Path)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	mode := os.FileMode(in.Mode)
	if mode == 0 {
		mode = 0o644
	}
	flags := os.O_CREATE | os.O_WRONLY
	if in.Append {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(in.Path, flags, mode)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	n, err := f.Write(data)
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": in.Path, "bytes": n}, nil
}

func fsPatch(_ context.Context, raw json.RawMessage) (any, error) {
	var in fsPatchInput
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if in.Path == "" || len(in.Edits) == 0 {
		return nil, fmt.Errorf("path and at least one edit are required")
	}
	data, err := os.ReadFile(in.Path)
	if err != nil {
		return nil, err
	}
	text := string(data)
	total := 0
	for i, e := range in.Edits {
		if e.Old == "" {
			return nil, fmt.Errorf("edit %d: old text is empty", i)
		}
		found := strings.Count(text, e.Old)
		if found == 0 {
			return nil, fmt.Errorf("edit %d: old text not found", i)
		}
		count := e.Count
		if count <= 0 || count > found {
			count = found
		}
		text = strings.Replace(text, e.Old, e.New, count)
		total += count
	}
	info, err := os.Stat(in.Path)
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(in.Path), ".cycom-patch-*")
	if err != nil {
		return nil, err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if _, err := io.WriteString(tmp, text); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(name, in.Path); err != nil {
		return nil, err
	}
	return map[string]any{"path": in.Path, "replacements": total}, nil
}

func fsMove(_ context.Context, raw json.RawMessage) (any, error) {
	var in fsMoveInput
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if in.Source == "" || in.Dest == "" {
		return nil, fmt.Errorf("source and dest are required")
	}
	if err := os.Rename(in.Source, in.Dest); err != nil {
		return nil, err
	}
	return map[string]any{"source": in.Source, "dest": in.Dest, "moved": true}, nil
}

func fsRemove(_ context.Context, raw json.RawMessage) (any, error) {
	var in fsRemoveInput
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if in.Path == "" {
		return nil, fmt.Errorf("path is required")
	}
	info, err := os.Lstat(in.Path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() && in.Recursive {
		err = os.RemoveAll(in.Path)
	} else {
		err = os.Remove(in.Path)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": in.Path, "removed": true}, nil
}

func fsSearch(ctx context.Context, raw json.RawMessage) (any, error) {
	var in fsSearchInput
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if in.Root == "" || in.Pattern == "" {
		return nil, fmt.Errorf("root and pattern are required")
	}
	re, err := regexp.Compile(in.Pattern)
	if err != nil {
		return nil, err
	}
	max := in.MaxResults
	if max <= 0 || max > 5000 {
		max = 200
	}
	maxFile := in.MaxFileBytes
	if maxFile <= 0 || maxFile > 64<<20 {
		maxFile = 4 << 20
	}
	matches := make([]map[string]any, 0)
	truncated := false
	walkErr := filepath.WalkDir(in.Root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if d.IsDir() {
			return nil
		}
		if in.Name != "" && !strings.Contains(d.Name(), in.Name) {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxFile {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		scan := bufio.NewScanner(f)
		scan.Buffer(make([]byte, 64<<10), 1<<20)
		line := 0
		for scan.Scan() {
			line++
			if re.MatchString(scan.Text()) {
				matches = append(matches, map[string]any{"path": path, "line": line, "text": scan.Text()})
				if len(matches) >= max {
					truncated = true
					return io.EOF
				}
			}
		}
		return nil
	})
	if walkErr != nil && walkErr != io.EOF {
		return nil, walkErr
	}
	return map[string]any{"matches": matches, "truncated": truncated}, nil
}

func fsStat(_ context.Context, raw json.RawMessage) (any, error) {
	var in fsPathInput
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if in.Path == "" {
		return nil, fmt.Errorf("path is required")
	}
	info, err := os.Lstat(in.Path)
	if os.IsNotExist(err) {
		return map[string]any{"path": in.Path, "exists": false}, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": in.Path, "exists": true, "type": fileType(info), "size": info.Size(), "mode": info.Mode().String(), "mod_time": info.ModTime()}, nil
}

func fileType(info os.FileInfo) string {
	switch {
	case info.IsDir():
		return "directory"
	case info.Mode()&os.ModeSymlink != 0:
		return "symlink"
	case info.Mode().IsRegular():
		return "file"
	default:
		return "other"
	}
}
