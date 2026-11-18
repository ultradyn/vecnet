//go:build windows

package platform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestTemporaryDirectoryOwnerMatchesPrivateStateContract(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "private")
	if err := MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	// Inheritance supplies a DACL, not an owner. An elevated token may default
	// ordinary MkdirTemp to Administrators; it must still fail private ownership.
	ordinary, err := os.MkdirTemp(parent, "ordinary-")
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(ordinary, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatal(err)
	}
	user, err := currentSID()
	if err != nil {
		t.Fatal(err)
	}
	err = CheckPath(ordinary, true, true)
	if windows.EqualSid(owner, user) {
		if err != nil {
			t.Fatal(err)
		}
	} else if err == nil || !strings.Contains(err.Error(), "owned by another Windows user") {
		t.Fatalf("foreign owner accepted or wrong refusal: %v", err)
	}
	private, err := MkdirTemp(parent, "private-")
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckPath(private, true, true); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateStateAndKernelLock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private state", "nested")
	if e := MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	if e := CheckPath(dir, true, true); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(dir, "authority.lock")
	a, e := OpenFile(p, os.O_RDWR|os.O_CREATE, 0600)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	unlock, e := Lock(context.Background(), a)
	if e != nil {
		t.Fatal(e)
	}
	b, e := OpenFile(p, os.O_RDWR, 0600)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, e = Lock(ctx, b); e == nil {
		t.Fatal("lock allowed concurrent owner")
	}
	if e = os.Remove(p); e == nil {
		t.Fatal("held lock path could be unlinked")
	}
	unlock()
	release, e := Lock(context.Background(), b)
	if e != nil {
		t.Fatal(e)
	}
	release()
}
func TestRefuseOtherUserACLAndHardlink(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if e := MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(dir, "authority.json")
	f, e := OpenFile(p, os.O_RDWR|os.O_CREATE, 0600)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	sd, e := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	if e != nil {
		t.Fatal(e)
	}
	acl, _, e := sd.DACL()
	if e != nil {
		t.Fatal(e)
	}
	if e = windows.SetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); e != nil {
		t.Fatal(e)
	}
	if e = CheckFile(f, false, true); e == nil {
		t.Fatal("public DACL accepted")
	}
	f.Close()
	p2 := filepath.Join(dir, "source")
	f, e = OpenFile(p2, os.O_RDWR|os.O_CREATE, 0600)
	if e != nil {
		t.Fatal(e)
	}
	f.Close()
	if e = os.Link(p2, filepath.Join(dir, "link")); e != nil {
		t.Fatal(e)
	}
	if _, e = OpenFile(p2, os.O_RDONLY, 0600); e == nil {
		t.Fatal("hard link authority accepted")
	}
}
func TestAtomicReplace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if e := MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	for _, p := range []string{a, b} {
		f, e := OpenFile(p, os.O_CREATE|os.O_WRONLY, 0600)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.WriteString(filepath.Base(p)); e != nil {
			t.Fatal(e)
		}
		f.Close()
	}
	if e := Replace(a, b); e != nil {
		t.Fatal(e)
	}
	got, e := os.ReadFile(b)
	if e != nil || string(got) != "a" {
		t.Fatalf("replace: %s %v", got, e)
	}
}

func TestOpenDirectoryPinsNamespace(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "parent")
	leaf := filepath.Join(parent, "leaf")
	if e := MkdirAll(leaf, 0700); e != nil {
		t.Fatal(e)
	}
	f, e := OpenDir(leaf)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if e = os.Rename(parent, parent+"-moved"); e == nil {
		t.Fatal("ancestor could be renamed while leaf was open")
	}
}
