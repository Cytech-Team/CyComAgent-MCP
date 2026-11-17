//go:build linux

package remotejobs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadBrokerReferenceConfigStrictOwnerOnlyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "references.json")
	data := `{"version":1,"executables":[{"digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","root_path":"/opt/workers","relative_path":"worker"}],"working_directories":[{"reference":"workspace_a","caller_node_id":"node-a","root_path":"/srv/jobs","relative_path":"node-a"}]}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadBrokerReferenceConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != 1 || len(cfg.Executables) != 1 || len(cfg.WorkingDirectories) != 1 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBrokerReferenceConfig(path); err == nil {
		t.Fatal("group/world-readable config accepted")
	}
}

func TestLoadBrokerReferenceConfigRejectsSymlinkUnknownAndDuplicateKeys(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	link := filepath.Join(dir, "link.json")
	if err := os.WriteFile(target, []byte(`{"version":1,"executables":[],"working_directories":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBrokerReferenceConfig(link); err == nil {
		t.Fatal("symlink config accepted")
	}
	for i, data := range []string{
		`{"version":1,"version":1,"executables":[],"working_directories":[]}`,
		`{"version":1,"executables":[],"working_directories":[],"allow_shell":true}`,
	} {
		path := filepath.Join(dir, string(rune('a'+i))+".json")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadBrokerReferenceConfig(path); err == nil {
			t.Fatalf("invalid config %d accepted", i)
		}
	}
}
