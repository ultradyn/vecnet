//go:build windows

package cli

import (
	"context"
	"io"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// FilePipeLocalInformation is documented by MS-FSCC, information class 24. It
// reports the actual write quota remaining on this handle (GetNamedPipeInfo
// reports only total capacity). A hook has exactly one writer; a reader can only
// increase that quota, so a single write after this check never stalls halfway.
type hookPipeInfo struct {
	Type, Configuration, MaximumInstances, CurrentInstances    uint32
	InboundQuota, ReadAvailable, OutboundQuota, WriteAvailable uint32
	State, End                                                 uint32
}

func hookWindowsPipeRoom(file *os.File) (int, error) {
	var info hookPipeInfo
	var ios windows.IO_STATUS_BLOCK
	e := windows.NtQueryInformationFile(windows.Handle(file.Fd()), &ios, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), 24)
	if e != nil {
		return 0, e
	}
	return int(info.WriteAvailable), nil
}
func hookAtomicPipe(w io.Writer) (func(context.Context, []byte) error, bool) {
	file, ok := w.(*os.File)
	if !ok {
		return nil, false
	}
	typ, e := windows.GetFileType(windows.Handle(file.Fd()))
	if e != nil || typ != windows.FILE_TYPE_PIPE {
		return nil, false
	}
	return func(ctx context.Context, data []byte) error {
		return hookGatedWrite(ctx, data, func() (int, error) { return hookWindowsPipeRoom(file) }, func(data []byte) (int, error) {
			var n uint32
			e := windows.WriteFile(windows.Handle(file.Fd()), data, &n, nil)
			return int(n), e
		})
	}, true
}
