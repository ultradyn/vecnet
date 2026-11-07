//go:build linux

package cli_test

import (
	"os"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

// prefsPTY returns a pseudoterminal master and two slave ends so stdin and
// stdout are both terminals. TIOCSPTLCK and TIOCGPTN are Linux pty ioctls.
func prefsPTY(t *testing.T) (master, stdin, stdout *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { master.Close() })
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { slave.Close() })
	dup, err := unix.Dup(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	stdout = os.NewFile(uintptr(dup), slave.Name())
	t.Cleanup(func() { stdout.Close() })
	return master, slave, stdout
}
