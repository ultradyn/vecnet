package tui

import (
	"encoding/binary"
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func chatDanglingLink(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Symlink(target, link); err == nil {
		return
	} else if !errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
		t.Fatal(err)
	}
	// A junction is an unprivileged directory reparse point, and may dangle
	// just like a symlink. It keeps the save test's no-follow/no-overwrite
	// assertion active without requiring Developer Mode or administrator rights.
	if err := os.Mkdir(link, 0o700); err != nil {
		t.Fatal(err)
	}
	p, err := windows.UTF16PtrFromString(link)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	substitute, err := windows.UTF16FromString("\\??\\" + target)
	if err != nil {
		t.Fatal(err)
	}
	printName, err := windows.UTF16FromString(target)
	if err != nil {
		t.Fatal(err)
	}
	names := append(substitute, printName...)
	// REPARSE_DATA_BUFFER's mount-point member: 8-byte outer header,
	// 8-byte name offsets/lengths, then the two NUL-terminated UTF-16 names.
	data := make([]byte, 16+2*len(names))
	binary.LittleEndian.PutUint32(data[0:4], windows.IO_REPARSE_TAG_MOUNT_POINT)
	binary.LittleEndian.PutUint16(data[4:6], uint16(len(data)-8))
	binary.LittleEndian.PutUint16(data[10:12], uint16(2*(len(substitute)-1)))
	binary.LittleEndian.PutUint16(data[12:14], uint16(2*len(substitute)))
	binary.LittleEndian.PutUint16(data[14:16], uint16(2*(len(printName)-1)))
	for i, c := range names {
		binary.LittleEndian.PutUint16(data[16+2*i:], c)
	}
	var returned uint32
	if err := windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT, &data[0], uint32(len(data)), nil, 0, &returned, nil); err != nil {
		t.Fatal(err)
	}
}
