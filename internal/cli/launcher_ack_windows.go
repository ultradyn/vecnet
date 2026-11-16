//go:build windows

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type launcherStartRequest struct {
	Schema           int    `json:"schema"`
	Token            string `json:"token"`
	Task             string `json:"task"`
	DefinitionSHA256 string `json:"definition_sha256"`
}
type launcherStartAck struct {
	launcherStartRequest
	PID         uint32 `json:"pid"`
	HostPID     uint32 `json:"host_pid"`
	HostCreated string `json:"host_created"`
	Node        string `json:"node"`
	Entry       string `json:"entry"`
	Config      string `json:"config"`
}

func launcherTaskStartRequest(def launcherTaskDefinition) launcherStartRequest {
	raw, _ := json.Marshal(def)
	return launcherStartRequest{Schema: 1, Token: launcherTempName(""), Task: def.Name, DefinitionSHA256: launcherHash(raw)}
}
func launcherAckMatches(ack launcherStartAck, want launcherStartRequest, def launcherTaskDefinition) bool {
	return ack.launcherStartRequest == want && ack.PID != 0 && ack.HostPID != 0 && ack.HostCreated != "" && strings.EqualFold(ack.Node, def.Executable) && ack.Entry == def.Args[1] && ack.Config == def.Args[4]
}
func launcherReadStartAck(root *launcherRoot, want launcherStartRequest, def launcherTaskDefinition) (launcherStartAck, error) {
	var ack launcherStartAck
	info, err := root.Lstat("launcher-active.json")
	if err != nil {
		return ack, err
	}
	if info.Size() > 64<<10 {
		return ack, errors.New("oversized launcher start acknowledgement")
	}
	raw, err := launcherManagedRead(root, "launcher-active.json")
	if err != nil {
		return ack, err
	}
	if err = launcherDecode(raw, &ack); err != nil {
		return ack, err
	}
	if !launcherAckMatches(ack, want, def) {
		return ack, errors.New("launcher acknowledgement does not match this start/definition")
	}
	return ack, nil
}

type launcherLiveProcesses struct{ child, host windows.Handle }

func (p launcherLiveProcesses) Close() { windows.CloseHandle(p.child); windows.CloseHandle(p.host) }
func (p launcherLiveProcesses) alive() bool {
	for _, handle := range []windows.Handle{p.child, p.host} {
		state, err := windows.WaitForSingleObject(handle, 0)
		if err != nil || state != uint32(windows.WAIT_TIMEOUT) {
			return false
		}
	}
	return true
}
func launcherProcessImage(handle windows.Handle) (string, error) {
	path := make([]uint16, 32768)
	n := uint32(len(path))
	err := windows.QueryFullProcessImageName(handle, 0, &path[0], &n)
	if err != nil {
		return "", err
	}
	return windows.UTF16ToString(path[:n]), nil
}
func launcherPinStartProcesses(ack launcherStartAck, powershell string) (launcherLiveProcesses, error) {
	var p launcherLiveProcesses
	var err error
	p.child, err = windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, ack.PID)
	if err != nil {
		return p, err
	}
	p.host, err = windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, ack.HostPID)
	if err != nil {
		windows.CloseHandle(p.child)
		return p, err
	}
	fail := func(err error) (launcherLiveProcesses, error) { p.Close(); return launcherLiveProcesses{}, err }
	childPath, err := launcherProcessImage(p.child)
	if err != nil {
		return fail(err)
	}
	hostPath, err := launcherProcessImage(p.host)
	if err != nil {
		return fail(err)
	}
	if !strings.EqualFold(childPath, ack.Node) || !strings.EqualFold(hostPath, powershell) {
		return fail(errors.New("launcher acknowledgement process image differs"))
	}
	var created, exit, kernel, user windows.Filetime
	if err = windows.GetProcessTimes(p.host, &created, &exit, &kernel, &user); err != nil {
		return fail(err)
	}
	ticks := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	if fmt.Sprint(ticks) != ack.HostCreated {
		return fail(errors.New("launcher host process identity changed"))
	}
	// Pin both process handles, then verify the acknowledged child really has
	// this host as its parent. PID reuse cannot satisfy the host creation check.
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return fail(err)
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	found := false
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if entry.ProcessID == ack.PID {
			found = entry.ParentProcessID == ack.HostPID
			break
		}
	}
	if !found || !p.alive() {
		return fail(errors.New("launcher acknowledgement is not a live scheduled child"))
	}
	return p, nil
}
func (c *launcherScheduler) waitForStart(ctx context.Context, before launcherSchedulerReply, request launcherStartRequest, def launcherTaskDefinition, root *launcherRoot) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for {
		final, err := c.inspect(ctx, def.Name)
		if err != nil {
			return err
		}
		if !final.Exists || final.Task != before.Task || final.Enabled != before.Enabled {
			return errors.New("launcher task changed during start verification")
		}
		ack, readErr := launcherReadStartAck(root, request, def)
		if readErr == nil && final.State == 4 && final.EnginePID == ack.HostPID && final.Instance != "" {
			processes, e := launcherPinStartProcesses(ack, c.powershell)
			if e == nil {
				// Task state 4 can precede Add-Type and Node startup. Require the same
				// fresh graph acknowledgement and child/host handles to survive this
				// interval, then re-query the exact scheduler instance before success.
				if !sleepCtx(ctx, 750*time.Millisecond) {
					processes.Close()
					return ctx.Err()
				}
				again, e := c.inspect(ctx, def.Name)
				ackNow, ackErr := launcherReadStartAck(root, request, def)
				stable := e == nil && ackErr == nil && ackNow == ack && again.State == 4 && again.Task == before.Task && again.Enabled == before.Enabled && again.EnginePID == final.EnginePID && again.Instance == final.Instance && processes.alive()
				processes.Close()
				if stable {
					return nil
				}
				if e != nil {
					return e
				}
				return errors.New("launcher scheduled child exited or changed during activation")
			}
		}
		if final.State != 4 && final.State != 2 {
			return errors.New("launcher task stopped before child startup acknowledgement")
		}
		if !sleepCtx(ctx, 100*time.Millisecond) {
			return fmt.Errorf("launcher startup acknowledgement pending: %w", ctx.Err())
		}
	}
}

// The bootstrap runs inside Node, not the scheduled PowerShell host. Ack is
// emitted only after the entry's module graph loads. It conveys software/process
// startup, not a hub credential, backend authority, or successful delivery.
const launcherWindowsBootstrap = `import {writeFileSync,renameSync} from 'node:fs';import {pathToFileURL} from 'node:url';
const d=JSON.parse(Buffer.from(process.env.VECNET_LAUNCHER_BOOTSTRAP,'base64').toString('utf8'));delete process.env.VECNET_LAUNCHER_BOOTSTRAP;
process.argv=[process.execPath,...d.args.slice(1)];
await import(pathToFileURL(d.args[1]).href);
const ack={...d.request,pid:process.pid,host_pid:d.host_pid,host_created:d.host_created,node:process.execPath,entry:d.args[1],config:d.args[4]};
const tmp=d.ack+'.'+process.pid+'.tmp';writeFileSync(tmp,JSON.stringify(ack),{flag:'wx',mode:0o600});renameSync(tmp,d.ack);
`
