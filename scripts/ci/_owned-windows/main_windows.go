// CI-only Windows process-tree owner. Build this package explicitly.
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type request struct {
	File         string   `json:"file"`
	Args         []string `json:"args"`
	Milliseconds int      `json:"ms"`
}

func main() {
	var req request
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "expected one CI owner request")
		os.Exit(1)
	}
	data, err := base64.StdEncoding.DecodeString(os.Args[1])
	if err == nil {
		err = json.Unmarshal(data, &req)
	}
	if err == nil && (!filepath.IsAbs(req.File) || req.Milliseconds < 1 || req.Milliseconds > 51500) {
		err = fmt.Errorf("expected absolute executable and budget of 1..51500 ms")
	}
	code := 1
	if err == nil {
		code, err = run(req)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}

func run(req request) (code int, err error) {
	app, err := windows.UTF16PtrFromString(req.File)
	if err != nil {
		return 1, err
	}
	command, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{req.File}, req.Args...)))
	if err != nil {
		return 1, err
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 1, err
	}
	defer windows.CloseHandle(job)
	if err := setKillOnClose(job); err != nil {
		return 1, err
	}
	process, err := createSuspended(app, command)
	if err != nil {
		return 1, fmt.Errorf("start %q: %w", req.File, err)
	}
	defer windows.CloseHandle(process.Process)
	defer windows.CloseHandle(process.Thread)
	if err := windows.AssignProcessToJobObject(job, process.Process); err != nil {
		// The unassigned child has never run, so there are no descendants.
		if windows.TerminateProcess(process.Process, 1) == nil {
			windows.WaitForSingleObject(process.Process, 2000)
		}
		return 1, fmt.Errorf("assign suspended child to job: %w", err)
	}
	defer func() {
		if drainErr := terminateAndDrain(job); drainErr != nil {
			code, err = 1, drainErr
		}
	}()
	if _, err := windows.ResumeThread(process.Thread); err != nil {
		return 1, fmt.Errorf("resume child: %w", err)
	}
	deadline := time.Now().Add(time.Duration(req.Milliseconds) * time.Millisecond)
	cancel := make(chan struct{})
	go func() {
		// A cancellation line or EOF (including supervisor death) ends the job.
		_, _ = bufio.NewReader(os.Stdin).ReadSlice('\n')
		close(cancel)
	}()
	for {
		state, err := windows.WaitForSingleObject(process.Process, 0)
		if err != nil {
			return 1, err
		}
		if state == windows.WAIT_OBJECT_0 {
			var exit uint32
			err := windows.GetExitCodeProcess(process.Process, &exit)
			return int(exit), err
		}
		select {
		case <-cancel:
			return 143, nil
		default:
		}
		left := time.Until(deadline)
		if left <= 0 {
			return 124, nil
		}
		time.Sleep(min(10*time.Millisecond, left))
	}
}

func createSuspended(app, command *uint16) (windows.ProcessInformation, error) {
	var process windows.ProcessInformation
	nul, err := windows.UTF16PtrFromString("NUL")
	if err != nil {
		return process, err
	}
	security := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	input, err := windows.CreateFile(nul, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, &security, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return process, err
	}
	defer windows.CloseHandle(input)
	// Explicitly allow only the child's three stdio handles. In particular
	// neither the job nor the owner's cancellation pipe can be inherited.
	handles := [3]windows.Handle{input}
	for i, stream := range []*os.File{os.Stdout, os.Stderr} {
		self := windows.CurrentProcess()
		if err := windows.DuplicateHandle(self, windows.Handle(stream.Fd()), self, &handles[i+1], 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
			return process, err
		}
		defer windows.CloseHandle(handles[i+1])
	}
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return process, err
	}
	defer attrs.Delete()
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), unsafe.Sizeof(handles)); err != nil {
		return process, err
	}
	startup := windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{})), Flags: windows.STARTF_USESTDHANDLES,
			StdInput: handles[0], StdOutput: handles[1], StdErr: handles[2],
		},
		ProcThreadAttributeList: attrs.List(),
	}
	// nil cwd and environment preserve the original inherited values.
	err = windows.CreateProcess(app, command, nil, nil, true,
		windows.CREATE_SUSPENDED|windows.CREATE_NO_WINDOW|windows.EXTENDED_STARTUPINFO_PRESENT,
		nil, nil, &startup.StartupInfo, &process)
	return process, err
}

func setKillOnClose(job windows.Handle) error {
	limits := new(windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION)
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	// The x/sys job information wrappers take uintptr, not Go pointers.
	var pinned runtime.Pinner
	pinned.Pin(limits)
	defer pinned.Unpin()
	_, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(limits)), uint32(unsafe.Sizeof(*limits)))
	return err
}

// Win32 JOBOBJECT_BASIC_ACCOUNTING_INFORMATION (not exposed by x/sys).
type jobAccounting struct {
	TotalUserTime, TotalKernelTime, PeriodUserTime, PeriodKernelTime int64
	PageFaults, TotalProcesses, ActiveProcesses, TerminatedProcesses uint32
}

func terminateAndDrain(job windows.Handle) error {
	killErr := windows.TerminateJobObject(job, 1)
	until := time.Now().Add(2 * time.Second)
	counts := new(jobAccounting)
	var pinned runtime.Pinner
	pinned.Pin(counts)
	defer pinned.Unpin()
	for {
		if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation,
			uintptr(unsafe.Pointer(counts)), uint32(unsafe.Sizeof(*counts)), nil); err != nil {
			return fmt.Errorf("query owned job: %w", err)
		}
		if counts.ActiveProcesses == 0 {
			return nil
		}
		if killErr != nil {
			return fmt.Errorf("terminate owned job: %w", killErr)
		}
		left := time.Until(until)
		if left <= 0 {
			return fmt.Errorf("owned job did not drain")
		}
		time.Sleep(min(10*time.Millisecond, left))
	}
}
