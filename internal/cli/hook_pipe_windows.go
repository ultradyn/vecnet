package cli

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func init() { hookPlatformPipe = hookWindowsPipe }

// Native FILE_INFORMATION_CLASS values and layouts are defined by the
// Windows SDK (ntifs.h). Keep the query structs entirely uint32 fields.
const (
	hookFilePipeInformation      = 23
	hookFilePipeLocalInformation = 24
)

type hookWindowsPipeInfo struct{ ReadMode, CompletionMode uint32 }
type hookWindowsPipeLocalInfo struct {
	NamedPipeType, NamedPipeConfiguration, MaximumInstances, CurrentInstances uint32
	InboundQuota, ReadDataAvailable, OutboundQuota, WriteQuotaAvailable       uint32
	NamedPipeState, NamedPipeEnd                                              uint32
}

func hookWindowsPipe(f *os.File) (*hookPipeWriter, func(), error) {
	process := windows.CurrentProcess()
	var handle windows.Handle
	err := windows.DuplicateHandle(process, windows.Handle(f.Fd()), process, &handle, 0, false, windows.DUPLICATE_SAME_ACCESS)
	if err != nil {
		return nil, nil, err
	}
	var status windows.IO_STATUS_BLOCK
	var original hookWindowsPipeInfo
	err = windows.NtQueryInformationFile(handle, &status, (*byte)(unsafe.Pointer(&original)), uint32(unsafe.Sizeof(original)), hookFilePipeInformation)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return nil, nil, err
	}
	// Inherited anonymous stdout pipes are synchronous and are not registered
	// with Go's I/O poller. Nonblocking completion plus a write-quota check
	// avoids needing an overlapped handle or a leaking write goroutine.
	nonblocking := original
	nonblocking.CompletionMode = 1 // FILE_PIPE_COMPLETE_OPERATION
	err = windows.NtSetInformationFile(handle, &status, (*byte)(unsafe.Pointer(&nonblocking)), uint32(unsafe.Sizeof(nonblocking)), hookFilePipeInformation)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return nil, nil, err
	}
	closeOutput := func() {
		_ = windows.NtSetInformationFile(handle, &status, (*byte)(unsafe.Pointer(&original)), uint32(unsafe.Sizeof(original)), hookFilePipeInformation)
		_ = windows.CloseHandle(handle)
	}
	limit, _ := hookPipeLimit("windows")
	w := &hookPipeWriter{limit: limit}
	w.available = func() (int, error) {
		var local hookWindowsPipeLocalInfo
		var status windows.IO_STATUS_BLOCK
		err := windows.NtQueryInformationFile(handle, &status, (*byte)(unsafe.Pointer(&local)), uint32(unsafe.Sizeof(local)), hookFilePipeLocalInformation)
		return int(local.WriteQuotaAvailable), err
	}
	w.write = func(data []byte) (int, error) {
		var written uint32
		err := windows.WriteFile(handle, data, &written, nil)
		return int(written), err
	}
	if _, err := w.available(); err != nil {
		closeOutput()
		return nil, nil, err
	}
	return w, closeOutput, nil
}
