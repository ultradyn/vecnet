//go:build windows

package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/XertroV/vecnet/internal/platform"
	"golang.org/x/sys/windows"
)

type launcherScheduler struct {
	powershell, sid string
	observed        map[string]launcherSchedulerReply
}
type launcherSchedulerTask struct {
	Name        string `json:"name"`
	SID         string `json:"sid"`
	Description string `json:"description"`
	Command     string `json:"command"`
	Arguments   string `json:"arguments"`
	Directory   string `json:"directory"`
}
type launcherSchedulerReply struct {
	Exists    bool                  `json:"exists"`
	Safe      bool                  `json:"safe"`
	Enabled   bool                  `json:"enabled"`
	State     int                   `json:"state"`
	XML       string                `json:"xml"`
	Task      launcherSchedulerTask `json:"task"`
	EnginePID uint32                `json:"engine_pid"`
	Instance  string                `json:"instance"`
}

func launcherNativeTaskController(*app) (launcherTaskController, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, err
	}
	return &launcherScheduler{powershell: filepath.Join(system, "WindowsPowerShell", "v1.0", "powershell.exe"), sid: user.User.Sid.String()}, nil
}
func launcherEncodePowerShell(script string) string {
	units := utf16.Encode([]rune(script))
	raw := make([]byte, len(units)*2)
	for i, u := range units {
		binary.LittleEndian.PutUint16(raw[i*2:], u)
	}
	return base64.StdEncoding.EncodeToString(raw)
}
func launcherDecodePowerShell(encoded string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw)%2 != 0 {
		return "", errors.New("invalid launcher host encoding")
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(raw[i*2:])
	}
	return string(utf16.Decode(units)), nil
}
func launcherTaskHost(def launcherTaskDefinition) string {
	raw, _ := json.Marshal(def)
	return "$launcherData='" + base64.StdEncoding.EncodeToString(raw) + "'\n$launcherBootstrap='" + base64.StdEncoding.EncodeToString([]byte(launcherWindowsBootstrap)) + "'\n" + launcherWindowsHost
}
func (c *launcherScheduler) definition(def launcherTaskDefinition) launcherSchedulerTask {
	return launcherSchedulerTask{Name: def.Name, SID: c.sid, Description: "vecnet managed profile launcher v1", Command: c.powershell, Arguments: "-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -WindowStyle Hidden -EncodedCommand " + launcherEncodePowerShell(launcherTaskHost(def)), Directory: def.Directory}
}
func (c *launcherScheduler) project(task launcherSchedulerTask) (launcherTaskDefinition, error) {
	prefix := "-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -WindowStyle Hidden -EncodedCommand "
	if task.SID != c.sid || task.Description != "vecnet managed profile launcher v1" || !strings.EqualFold(task.Command, c.powershell) || !strings.HasPrefix(task.Arguments, prefix) {
		return launcherTaskDefinition{}, errors.New("launcher scheduler action changed")
	}
	script, err := launcherDecodePowerShell(strings.TrimPrefix(task.Arguments, prefix))
	if err != nil {
		return launcherTaskDefinition{}, err
	}
	header, _, ok := strings.Cut(script, "\n")
	if !ok || !strings.HasPrefix(header, "$launcherData='") || !strings.HasSuffix(header, "'") {
		return launcherTaskDefinition{}, errors.New("launcher host changed")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(strings.TrimPrefix(header, "$launcherData='"), "'"))
	if err != nil {
		return launcherTaskDefinition{}, err
	}
	def, err := launcherDecodeTask(raw)
	if err != nil {
		return def, err
	}
	if script != launcherTaskHost(def) || task != c.definition(def) {
		return def, errors.New("launcher host or task definition changed")
	}
	return def, nil
}
func (c *launcherScheduler) command(ctx context.Context, op string, task launcherSchedulerTask, expected string, enabled bool) (launcherSchedulerReply, error) {
	data, _ := json.Marshal(struct {
		Op       string                `json:"op"`
		Task     launcherSchedulerTask `json:"task"`
		Expected string                `json:"expected"`
		Enabled  bool                  `json:"enabled"`
	}{op, task, expected, enabled})
	// Keep data on stdin: nested encoded host arguments otherwise exceed the
	// Windows 32 KiB CreateProcess command-line limit during registration.
	script := "$ProgressPreference='SilentlyContinue';$launcherInput=[Console]::In.ReadToEnd()\n" + launcherSchedulerScript
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-EncodedCommand", launcherEncodePowerShell(script))
	cmd.Stdin = strings.NewReader(base64.StdEncoding.EncodeToString(data))
	platform.Background(cmd)
	cmd.WaitDelay = 100 * time.Millisecond
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return launcherSchedulerReply{}, ctx.Err()
		}
		return launcherSchedulerReply{}, fmt.Errorf("launcher Task Scheduler %s failed: %w: %s", op, err, strings.TrimSpace(stderr.String()))
	}
	var reply launcherSchedulerReply
	raw = bytes.TrimPrefix(bytes.TrimSpace(raw), []byte("\xef\xbb\xbf"))
	if len(raw) > 1<<20 {
		return reply, errors.New("oversized launcher Task Scheduler response")
	}
	if err := launcherDecode(raw, &reply); err != nil {
		return reply, fmt.Errorf("uncertain launcher Task Scheduler response: %w: %.500s", err, raw)
	}
	if reply.Exists && !reply.Safe {
		return reply, fmt.Errorf("foreign-owned, custom or unsafe launcher task; preserve task (projection=%t): %s", reply.Task.Name != "", stderr.String())
	}
	return reply, nil
}
func (c *launcherScheduler) inspect(ctx context.Context, name string) (launcherSchedulerReply, error) {
	if !strings.HasPrefix(name, "vecnet-launcher-p-") || !launcherTaskHex(strings.TrimPrefix(name, "vecnet-launcher-p-")) {
		return launcherSchedulerReply{}, errors.New("invalid launcher task name")
	}
	return c.command(ctx, "inspect", launcherSchedulerTask{Name: name, SID: c.sid}, "", false)
}
func (c *launcherScheduler) Inspect(ctx context.Context, name string) (launcherTaskState, error) {
	reply, err := c.inspect(ctx, name)
	if err != nil {
		return launcherTaskState{}, err
	}
	if c.observed == nil {
		c.observed = map[string]launcherSchedulerReply{}
	}
	c.observed[name] = reply
	state := launcherTaskState{Exists: reply.Exists, Active: reply.State == 4, Enabled: reply.Enabled}
	if !reply.Exists {
		return state, nil
	}
	if reply.State != 1 && reply.State != 3 && reply.State != 4 {
		return state, errors.New("launcher Task Scheduler state is uncertain")
	}
	state.Definition, err = c.project(reply.Task)
	return state, err
}
func (c *launcherScheduler) Register(ctx context.Context, def launcherTaskDefinition, enabled bool) error {
	if err := launcherValidateTask(def); err != nil {
		return err
	}
	release, err := launcherPinExecutable(def.Executable)
	if err != nil {
		return err
	}
	defer release()
	releaseBin, err := launcherPinExecutable(def.Environment["VECNET_BIN"])
	if err != nil {
		return err
	}
	defer releaseBin()
	before, err := c.inspect(ctx, def.Name)
	if err != nil {
		return err
	}
	if observed, ok := c.observed[def.Name]; ok && (observed.XML != before.XML || observed.State != before.State) {
		return errors.New("launcher task changed after inspection")
	}
	if before.Exists {
		if _, err = c.project(before.Task); err != nil {
			return err
		}
	}
	reply, err := c.command(ctx, "register", c.definition(def), before.XML, enabled)
	if err != nil {
		return err
	}
	if !reply.Exists || reply.Enabled != enabled || reply.Task != c.definition(def) {
		return errors.New("launcher task registration verification failed")
	}
	if c.observed == nil {
		c.observed = map[string]launcherSchedulerReply{}
	}
	c.observed[def.Name] = reply
	return nil
}
func (c *launcherScheduler) Restart(ctx context.Context, name string) error {
	before, err := c.inspect(ctx, name)
	if err != nil {
		return err
	}
	if !before.Exists {
		return errors.New("launcher task is missing")
	}
	if observed, ok := c.observed[name]; ok && (observed.XML != before.XML || observed.State != before.State) {
		return errors.New("launcher task changed before restart")
	}
	def, err := c.project(before.Task)
	if err != nil {
		return err
	}
	release, err := launcherPinExecutable(def.Executable)
	if err != nil {
		return err
	}
	defer release()
	releaseBin, err := launcherPinExecutable(def.Environment["VECNET_BIN"])
	if err != nil {
		return err
	}
	defer releaseBin()
	root, err := launcherOpenDir(filepath.Dir(def.Args[4]), filepath.Dir(def.Args[4]), false)
	if err != nil {
		return err
	}
	defer root.Close()
	request := launcherTaskStartRequest(def)
	if err = launcherDurableWrite(root, "launcher-start.json", launcherManagedJSON(request), 0o600); err != nil {
		return err
	}
	if _, err = c.command(ctx, "restart", before.Task, before.XML, before.Enabled); err != nil {
		return err
	}
	return c.waitForStart(ctx, before, request, def, root)
}

// Task values are transported as JSON data. No command, path, environment or
// argument is interpolated as PowerShell code. Every mutation CASes inspected XML.
const launcherSchedulerScript = `$ErrorActionPreference='Stop'
[Console]::OutputEncoding=New-Object System.Text.UTF8Encoding($false)
$i=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($launcherInput))|ConvertFrom-Json
$d=$i.task
$s=New-Object -ComObject Schedule.Service;$s.Connect();$f=$s.GetFolder('\')
function SIDValue($v){try{return (New-Object Security.Principal.SecurityIdentifier($v)).Value}catch{try{return (New-Object Security.Principal.NTAccount($v)).Translate([Security.Principal.SecurityIdentifier]).Value}catch{return ''}}}
function SafeTask($t){
 try{$sd=New-Object Security.AccessControl.CommonSecurityDescriptor($false,$false,$t.GetSecurityDescriptor(7));if($sd.Owner.Value -ne $d.sid -or $null -eq $sd.DiscretionaryAcl){return $false}
 foreach($a in $sd.DiscretionaryAcl){if($a.AceQualifier -eq [Security.AccessControl.AceQualifier]::AccessAllowed -and $a.SecurityIdentifier.Value -notin @($d.sid,'S-1-5-18','S-1-5-32-544')){return $false}};return $true}catch{return $false}
}
function ProjectTask($t){
 $x=$t.Definition;$z=$x.Settings
 $ok=(SIDValue $x.Principal.UserId) -eq $d.sid -and $x.Principal.LogonType -eq 3 -and $x.Principal.RunLevel -eq 0 -and $x.Actions.Count -eq 1 -and $x.Triggers.Count -eq 1
 if($ok){$a=$x.Actions.Item(1);$tr=$x.Triggers.Item(1);$ok=$a.Type -eq 0 -and $tr.Type -eq 9 -and (SIDValue $tr.UserId) -eq $d.sid -and $tr.Enabled -and ([string]$tr.Delay) -in @('','PT0S') -and ([string]$tr.StartBoundary) -eq '' -and ([string]$tr.EndBoundary) -eq '' -and ([string]$tr.Repetition.Interval) -eq ''}
 $ok=$ok -and $z.MultipleInstances -eq 2 -and !$z.DisallowStartIfOnBatteries -and !$z.StopIfGoingOnBatteries -and $z.AllowHardTerminate -and $z.StartWhenAvailable -and !$z.RunOnlyIfNetworkAvailable -and $z.AllowDemandStart -and $z.Hidden -and !$z.RunOnlyIfIdle -and !$z.WakeToRun -and $z.ExecutionTimeLimit -eq 'PT0S' -and $z.RestartInterval -eq 'PT1M' -and $z.RestartCount -eq 999
 if(!$ok){return $null};return @{name=$t.Name;sid=(SIDValue $x.Principal.UserId);description=$x.RegistrationInfo.Description;command=$a.Path;arguments=$a.Arguments;directory=$a.WorkingDirectory}
}
$found=@($f.GetTasks(1)|Where-Object {$_.Name -eq $d.name});if($found.Count -gt 1){throw 'ambiguous task'};$t=$null;if($found.Count -eq 1){$t=$found[0]}
if($i.op -ne 'inspect'){
 if($t){if(!(SafeTask $t) -or !(ProjectTask $t) -or $t.Xml -cne $i.expected){throw 'launcher task changed after inspection'}}elseif($i.expected -ne ''){throw 'launcher task disappeared'}
 switch($i.op){
 'register'{
 $x=$s.NewTask(0);$x.RegistrationInfo.Author=$d.sid;$x.RegistrationInfo.Description=$d.description;$x.Principal.UserId=$d.sid;$x.Principal.LogonType=3;$x.Principal.RunLevel=0
 $tr=$x.Triggers.Create(9);$tr.UserId=$d.sid;$tr.Enabled=$true
 $z=$x.Settings;$z.MultipleInstances=2;$z.DisallowStartIfOnBatteries=$false;$z.StopIfGoingOnBatteries=$false;$z.AllowHardTerminate=$true;$z.StartWhenAvailable=$true;$z.RunOnlyIfNetworkAvailable=$false;$z.AllowDemandStart=$true;$z.Enabled=[bool]$i.enabled;$z.Hidden=$true;$z.RunOnlyIfIdle=$false;$z.WakeToRun=$false;$z.ExecutionTimeLimit='PT0S';$z.RestartInterval='PT1M';$z.RestartCount=999
 $a=$x.Actions.Create(0);$a.Path=$d.command;$a.Arguments=$d.arguments;$a.WorkingDirectory=$d.directory
 $sddl='O:'+$d.sid+'G:'+$d.sid+'D:P(A;;GA;;;'+$d.sid+')(A;;GA;;;SY)(A;;GA;;;BA)';$flags=2;if($t){$flags=4};$t=$f.RegisterTaskDefinition($d.name,$x,$flags,$d.sid,$null,3,$sddl)
 }
 'stop'{if($t){$t.Stop(0)}}
 'restart'{
 if(!$t){throw 'missing launcher task'};$t.Stop(0);$deadline=[DateTime]::UtcNow.AddSeconds(6);do{$t=$f.GetTask($d.name);if($t.State -ne 4 -and $t.State -ne 2){break};Start-Sleep -Milliseconds 100}while([DateTime]::UtcNow -lt $deadline);if($t.State -eq 4 -or $t.State -eq 2){throw 'launcher task did not stop'}
 $wasEnabled=[bool]$t.Enabled;try{if(!$wasEnabled){$t.Enabled=$true};[void]$t.Run($null)}finally{if(!$wasEnabled){$t.Enabled=$false}}
 }
 default{throw 'unknown launcher operation'}
 };$t=$f.GetTask($d.name)
}
$engine=0;$instance='';if($t -and $t.State -eq 4){$runs=@($s.GetRunningTasks(1)|Where-Object {$_.Path -eq ('\'+$d.name)});if($runs.Count -eq 1){$engine=[int]$runs[0].EnginePID;$instance=[string]$runs[0].InstanceGuid}}
if(!$t){@{engine_pid=0;instance='';exists=$false;safe=$true;enabled=$false;state=0;xml='';task=$null}|ConvertTo-Json -Compress -Depth 8}
else{$p=ProjectTask $t;@{engine_pid=$engine;instance=$instance;exists=$true;safe=((SafeTask $t) -and $null -ne $p);enabled=[bool]$t.Enabled;state=[int]$t.State;xml=[string]$t.Xml;task=$p}|ConvertTo-Json -Compress -Depth 8}
`

// The host joins its own kill-on-close job before starting Node. Descendants
// inherit containment from birth, so a scheduler stop cannot orphan backends.
const launcherWindowsHost = `$ErrorActionPreference='Stop'
$d=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($launcherData))|ConvertFrom-Json
$source=@"
using System;using System.Diagnostics;using System.Runtime.InteropServices;
public static class VecnetLauncherJob {
 [StructLayout(LayoutKind.Sequential)] struct Basic {public Int64 A,B;public UInt32 Flags;public UIntPtr C,D;public UInt32 E;public UIntPtr F;public UInt32 G,H;}
 [StructLayout(LayoutKind.Sequential)] struct IO {public UInt64 A,B,C,D,E,F;}
 [StructLayout(LayoutKind.Sequential)] struct Extended {public Basic Basic;public IO IO;public UIntPtr A,B,C,D;}
 [DllImport("kernel32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern IntPtr CreateJobObject(IntPtr a,string n);
 [DllImport("kernel32.dll",SetLastError=true)] static extern bool SetInformationJobObject(IntPtr j,int c,ref Extended i,uint n);
 [DllImport("kernel32.dll",SetLastError=true)] static extern bool AssignProcessToJobObject(IntPtr j,IntPtr p);
 [DllImport("kernel32.dll")] public static extern bool CloseHandle(IntPtr h);
 public static IntPtr Enter(){var h=CreateJobObject(IntPtr.Zero,null);if(h==IntPtr.Zero)throw new System.ComponentModel.Win32Exception();var x=new Extended();x.Basic.Flags=0x2000;if(!SetInformationJobObject(h,9,ref x,(uint)Marshal.SizeOf(x))||!AssignProcessToJobObject(h,Process.GetCurrentProcess().Handle)){CloseHandle(h);throw new System.ComponentModel.Win32Exception();}return h;}
}
"@
Add-Type -TypeDefinition $source
function QuoteArg([string]$s){return '"'+[regex]::Replace([regex]::Replace($s,'(\\*)"','$1$1\"'),'(\\+)$','$1$1')+'"'}
$profile=Split-Path -Parent $d.args[4];$requestPath=Join-Path $profile 'launcher-start.json';$definitionSHA=[Security.Cryptography.SHA256]::Create();try{$definitionHash=([BitConverter]::ToString($definitionSHA.ComputeHash([Convert]::FromBase64String($launcherData)))).Replace('-','').ToLowerInvariant()}finally{$definitionSHA.Dispose()}
$request=@{schema=1;token=[Guid]::NewGuid().ToString('N');task=$d.name;definition_sha256=$definitionHash}
if(Test-Path -LiteralPath $requestPath){$candidate=[IO.File]::ReadAllText($requestPath)|ConvertFrom-Json;if($candidate.schema -eq 1 -and $candidate.task -ceq $d.name -and $candidate.definition_sha256 -ceq $definitionHash){$request=$candidate}}
$boot=@{args=$d.args;request=$request;host_pid=$PID;host_created=([Diagnostics.Process]::GetCurrentProcess().StartTime.ToUniversalTime().ToFileTimeUtc().ToString());ack=(Join-Path $profile 'launcher-active.json')}|ConvertTo-Json -Compress -Depth 8
$job=[VecnetLauncherJob]::Enter();$p=New-Object Diagnostics.Process
try{
 $p.StartInfo.FileName=$d.executable;$p.StartInfo.Arguments='--disable-warning=ExperimentalWarning --input-type=module --eval '+(QuoteArg ([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($launcherBootstrap))));$p.StartInfo.WorkingDirectory=$d.directory;$p.StartInfo.UseShellExecute=$false;$p.StartInfo.CreateNoWindow=$true
 $p.StartInfo.EnvironmentVariables.Clear();foreach($v in $d.environment.PSObject.Properties){$p.StartInfo.EnvironmentVariables[$v.Name]=[string]$v.Value}
 $p.StartInfo.EnvironmentVariables['VECNET_LAUNCHER_BOOTSTRAP']=[Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($boot))
 foreach($key in @('SystemRoot','WINDIR','TEMP','TMP')){if([Environment]::GetEnvironmentVariable($key)){$p.StartInfo.EnvironmentVariables[$key]=[Environment]::GetEnvironmentVariable($key)}}
 if(!$p.Start()){throw 'cannot start launcher'};$p.WaitForExit();$code=$p.ExitCode
}catch{[Console]::Error.WriteLine('managed launcher host failed');$code=1}
finally{$p.Dispose()}
# Keep the job handle until process teardown: closing a kill-on-close job while
# its host is still inside it would destroy the intended Node exit status.
# Windows closes the handle on host exit/termination, containing all descendants.
exit $code
`

func launcherNativeProfileTasks(ctx context.Context) ([]string, error) {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	script := "$ErrorActionPreference='Stop';$ProgressPreference='SilentlyContinue';[Console]::OutputEncoding=New-Object Text.UTF8Encoding($false);$s=New-Object -ComObject Schedule.Service;$s.Connect();$names=@($s.GetFolder('\\').GetTasks(1)|Where-Object {$_.Name -match '^vecnet-launcher-p-[0-9a-f]{16}$'}|ForEach-Object {$_.Name});ConvertTo-Json -InputObject $names -Compress"
	cmd := exec.CommandContext(ctx, filepath.Join(system, "WindowsPowerShell", "v1.0", "powershell.exe"), "-NoLogo", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-EncodedCommand", launcherEncodePowerShell(script))
	platform.Background(cmd)
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("cannot inspect Windows launcher tasks: %w", err)
	}
	return launcherDecodeTaskNames(bytes.TrimPrefix(bytes.TrimSpace(raw), []byte("\xef\xbb\xbf")))
}
