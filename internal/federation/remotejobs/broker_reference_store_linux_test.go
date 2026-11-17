//go:build linux

package remotejobs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStaticBrokerDescriptorResolverUsesReceiverOwnedReferences(t *testing.T) {
	_, _, caller, req, resolved := brokerAuthorizationFixture(t)
	root := filepath.Dir(resolved.Executable)
	resolver, err := NewStaticBrokerDescriptorResolver(
		[]BrokerExecutableReference{{Digest: req.ExecutableDigest, RootPath: root, RelativePath: filepath.Base(resolved.Executable)}},
		[]BrokerWorkingDirectoryReference{{Reference: req.WorkingDirRef, CallerNodeID: caller.NodeID(), RootPath: root, RelativePath: filepath.Base(resolved.WorkingDir)}},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()
	got, err := resolver.ResolveBrokerDescriptors(caller, req)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Executable.Close()
	defer got.WorkingDir.Close()
	if got.Request.Executable != resolved.Executable || got.Request.WorkingDir != resolved.WorkingDir {
		t.Fatalf("unexpected receiver-resolved paths: %+v", got.Request)
	}
	if got.Request.Timeout != resolved.Timeout || got.Request.Args[0] != "--version" {
		t.Fatalf("unexpected resolved invocation: %+v", got.Request)
	}
	other := FederationCallerContext{nodeID: "node-other"}
	if _, err := resolver.ResolveBrokerDescriptors(other, req); err == nil {
		t.Fatal("reference was usable by a different caller")
	}
	if err := resolver.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ResolveBrokerDescriptors(caller, req); err == nil {
		t.Fatal("closed resolver accepted a request")
	}
}

func TestStaticBrokerDescriptorResolverRejectsSymlinkAndUnknownDigest(t *testing.T) {
	_, _, caller, req, resolved := brokerAuthorizationFixture(t)
	root := filepath.Dir(resolved.Executable)
	if err := os.Symlink(filepath.Base(resolved.Executable), filepath.Join(root, "worker-link")); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewStaticBrokerDescriptorResolver(
		[]BrokerExecutableReference{{Digest: req.ExecutableDigest, RootPath: root, RelativePath: "worker-link"}},
		[]BrokerWorkingDirectoryReference{{Reference: req.WorkingDirRef, CallerNodeID: caller.NodeID(), RootPath: root, RelativePath: filepath.Base(resolved.WorkingDir)}},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()
	if _, err := resolver.ResolveBrokerDescriptors(caller, req); err == nil {
		t.Fatal("symlink executable reference was accepted")
	}
	unknown := req
	unknown.ExecutableDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := resolver.ResolveBrokerDescriptors(caller, unknown); err == nil {
		t.Fatal("unknown executable digest was accepted")
	}
}

func TestStaticBrokerDescriptorResolverRejectsWritableReferenceRoot(t *testing.T) {
	_, _, caller, req, resolved := brokerAuthorizationFixture(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0777); err != nil {
		t.Fatal(err)
	}
	_, err := NewStaticBrokerDescriptorResolver(
		[]BrokerExecutableReference{{Digest: req.ExecutableDigest, RootPath: root, RelativePath: filepath.Base(resolved.Executable)}},
		[]BrokerWorkingDirectoryReference{{Reference: req.WorkingDirRef, CallerNodeID: caller.NodeID(), RootPath: root, RelativePath: filepath.Base(resolved.WorkingDir)}},
	)
	if err == nil {
		t.Fatal("group/world-writable reference root was accepted")
	}
}
