//go:build linux

package remotejobs

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func makeBrokerSocket(t *testing.T, dirMode, socketMode os.FileMode) (string, BrokerSocketPolicy) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, dirMode); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "broker.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := os.Chmod(path, socketMode); err != nil {
		t.Fatal(err)
	}
	uid := uint32(os.Getuid())
	return path, BrokerSocketPolicy{SocketUID: uid, ParentUID: uid}
}

func TestValidateBrokerSocketPathAcceptsPrivateOwnedSocket(t *testing.T) {
	path, policy := makeBrokerSocket(t, 0700, 0600)
	if err := ValidateBrokerSocketPath(path, policy); err != nil {
		t.Fatal(err)
	}
}

func TestValidateBrokerSocketPathRejectsUnsafeModesAndOwners(t *testing.T) {
	tests := []struct {
		name                string
		dirMode, socketMode os.FileMode
		edit                func(*BrokerSocketPolicy)
	}{
		{name: "writable parent", dirMode: 0770, socketMode: 0600},
		{name: "writable socket", dirMode: 0700, socketMode: 0660},
		{name: "wrong socket owner", dirMode: 0700, socketMode: 0600, edit: func(p *BrokerSocketPolicy) { p.SocketUID++ }},
		{name: "wrong parent owner", dirMode: 0700, socketMode: 0600, edit: func(p *BrokerSocketPolicy) { p.ParentUID++ }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, policy := makeBrokerSocket(t, tt.dirMode, tt.socketMode)
			if tt.edit != nil {
				tt.edit(&policy)
			}
			if err := ValidateBrokerSocketPath(path, policy); err == nil {
				t.Fatal("unsafe socket accepted")
			}
		})
	}
}

func TestValidateBrokerSocketPathRejectsNonSocketAndNonCanonicalPaths(t *testing.T) {
	dir := t.TempDir()
	policy := BrokerSocketPolicy{SocketUID: uint32(os.Getuid()), ParentUID: uint32(os.Getuid())}
	if err := ValidateBrokerSocketPath(filepath.Join(dir, "missing.sock"), policy); err == nil {
		t.Fatal("accepted missing socket")
	}
	if err := ValidateBrokerSocketPath(filepath.Join(dir, "..", filepath.Base(dir), "x"), policy); err == nil {
		t.Fatal("accepted non-canonical path")
	}
	file := filepath.Join(dir, "regular")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBrokerSocketPath(file, policy); err == nil {
		t.Fatal("accepted regular file")
	}
}

func TestValidateBrokerSocketPathPinnedRetainsValidatedSocketInode(t *testing.T) {
	path, policy := makeBrokerSocket(t, 0700, 0600)
	validation, err := ValidateBrokerSocketPathPinned(path, policy)
	if err != nil {
		t.Fatal(err)
	}
	defer validation.Close()
	pinnedBefore, err := validation.socket.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	pinnedAfter, err := validation.socket.Stat()
	if err != nil {
		t.Fatal(err)
	}
	current, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(pinnedBefore, pinnedAfter) {
		t.Fatal("pinned descriptor changed inode after pathname replacement")
	}
	if os.SameFile(pinnedAfter, current) {
		t.Fatal("test did not replace the pathname inode")
	}
}

func TestValidateBrokerSocketPathPinnedRejectsSymlinkedAncestor(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(real, "root.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	uid := uint32(os.Getuid())
	if _, err := ValidateBrokerSocketPathPinned(filepath.Join(link, "root.sock"), BrokerSocketPolicy{SocketUID: uid, ParentUID: uid}); err == nil {
		t.Fatal("accepted socket beneath symlinked ancestor")
	}
}
