//go:build linux

package remotejobs

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

func TestCreateExecutableSnapshotCopiesAuthorizedBytes(t *testing.T) {
	f, err := os.Open("/bin/sh")
	if err != nil {
		t.Skip(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, info.Size())
	if _, err := f.ReadAt(data, 0); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	snapshot, err := CreateExecutableSnapshot(f, hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Size() != info.Size() || snapshot.Digest() != hex.EncodeToString(sum[:]) {
		t.Fatalf("unexpected snapshot metadata: size=%d digest=%s", snapshot.Size(), snapshot.Digest())
	}
	got := make([]byte, len(data))
	if _, err := snapshot.Open().ReadAt(got, 0); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatal("snapshot bytes differ from authorized executable")
	}
	got[0] ^= 0xff
	verify := make([]byte, len(data))
	if _, err := snapshot.Open().ReadAt(verify, 0); err != nil {
		t.Fatal(err)
	}
	if string(verify) != string(data) {
		t.Fatal("snapshot exposed mutable backing storage")
	}
}

func TestCreateExecutableSnapshotRejectsDigestMismatchAndOversize(t *testing.T) {
	f, err := os.Open("/bin/sh")
	if err != nil {
		t.Skip(err)
	}
	defer f.Close()
	if _, err := CreateExecutableSnapshot(f, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err == nil {
		t.Fatal("accepted mismatched digest")
	}
	if _, err := CreateExecutableSnapshot(f, "bad"); err == nil {
		t.Fatal("accepted malformed digest")
	}
}
