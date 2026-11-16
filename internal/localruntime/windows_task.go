//go:build windows

package localruntime

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

type windowsTask struct {
	Schema      int    `json:"schema"`
	Name        string `json:"name"`
	SID         string `json:"sid"`
	Description string `json:"description"`
	Command     string `json:"command"`
	Arguments   string `json:"arguments"`
	Directory   string `json:"directory"`
}
type taskState struct {
	Exists     bool        `json:"exists"`
	Safe       bool        `json:"safe"`
	Enabled    bool        `json:"enabled"`
	State      int         `json:"state"`
	XML        string      `json:"xml"`
	Definition windowsTask `json:"definition"`
	Conflicts  []string    `json:"conflicts"`
}

func windowsSID() (string, error) {
	u, e := windows.GetCurrentProcessToken().GetTokenUser()
	if e != nil {
		return "", e
	}
	return u.User.Sid.String(), nil
}
func systemDirectory() (string, error) { return windows.GetSystemDirectory() }
func powershellPath() (string, error) {
	dir, e := systemDirectory()
	if e != nil {
		return "", e
	}
	return filepath.Join(dir, "WindowsPowerShell/v1.0/powershell.exe"), nil
}
func windowsRoot() (string, error) { d, e := systemDirectory(); return filepath.Dir(d), e }
func taskName(home, sid string) string {
	return "vecnet-codex-delivery-" + sum([]byte(strings.ToLower(home) + "\x00" + sid))[:24]
}
func windowsQuote(s string) string {
	// Windows CommandLineToArgvW quoting; no cmd.exe or expandable shell string.
	var b strings.Builder
	b.WriteByte('"')
	n := 0
	for _, r := range s {
		if r == '\\' {
			n++
			continue
		}
		if r == '"' {
			b.WriteString(strings.Repeat("\\", n*2+1))
			b.WriteRune(r)
		} else {
			b.WriteString(strings.Repeat("\\", n))
			b.WriteRune(r)
		}
		n = 0
	}
	b.WriteString(strings.Repeat("\\", n*2))
	b.WriteByte('"')
	return b.String()
}
func windowsDefinition(home, node, release string) ([]byte, error) {
	sid, e := windowsSID()
	if e != nil {
		return nil, e
	}
	ps, e := powershellPath()
	if e != nil {
		return nil, e
	}
	wr, e := windowsRoot()
	if e != nil {
		return nil, e
	}
	args := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-File", filepath.Join(release, "windows-host.ps1"), "-Node", node, "-HomePath", home, "-WindowsRoot", wr, "-ReleasePath", release}
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = windowsQuote(a)
	}
	return json.MarshalIndent(windowsTask{1, taskName(home, sid), sid, "vecnet managed standalone Codex delivery v1", ps, strings.Join(q, " "), release}, "", "  ")
}
func windowsRunner(o Options) func(context.Context, string, ...string) ([]byte, error) {
	wr, _ := windowsRoot()
	sys, _ := systemDirectory()
	env := []string{"HOME=" + o.Home, "USERPROFILE=" + o.Home, "SystemRoot=" + wr, "WINDIR=" + wr, "PATH=" + filepath.Dir(o.Node) + ";" + sys, "VECNET_LOCAL_MAINTENANCE=0"}
	// Add-Type needs a writable per-user compiler temp directory. Only these
	// explicit OS paths are allowed through; application secrets never inherit.
	for _, key := range []string{"TEMP", "TMP"} {
		if v := o.Getenv(key); v != "" {
			env = append(env, key+"="+v)
		}
	}
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if !filepath.IsAbs(name) {
			return nil, errors.New("Windows runtime requires absolute executable paths")
		}
		c := exec.CommandContext(ctx, name, args...)
		c.Env = env
		c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
		return c.CombinedOutput()
	}
}
func encodePowerShell(script string) string {
	units := utf16.Encode([]rune(script))
	b := make([]byte, len(units)*2)
	for i, u := range units {
		binary.LittleEndian.PutUint16(b[i*2:], u)
	}
	return base64.StdEncoding.EncodeToString(b)
}
func taskCommand(ctx context.Context, o Options, op string, definition windowsTask, expected string) (taskState, error) {
	input, _ := json.Marshal(struct {
		Op         string      `json:"op"`
		Definition windowsTask `json:"definition"`
		Expected   string      `json:"expected"`
	}{op, definition, taskXMLHash(expected)})
	script := "$inputData='" + base64.StdEncoding.EncodeToString(input) + "'\n" + taskScript
	ps, e := powershellPath()
	if e != nil {
		return taskState{}, e
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	b, e := o.Run(ctx, ps, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-EncodedCommand", encodePowerShell(script))
	if e != nil {
		return taskState{}, fmt.Errorf("managed delivery Task Scheduler %s failed; setup pending, retry repair", op)
	}
	var state taskState
	if len(b) > 1<<20 || decodePrivateJSON(bytesWithoutBOM(b), &state) != nil {
		return state, errors.New("invalid Task Scheduler response; setup pending")
	}
	if len(state.Conflicts) > 0 {
		return state, errors.New("conflicting running launcher or legacy delivery task; inspect and stop or migrate it before managed delivery activation")
	}
	if state.Exists && !state.Safe {
		return state, errors.New("custom, foreign-owned or unsafe managed delivery task; refusing replacement")
	}
	return state, nil
}
func taskXMLHash(xml string) string {
	if xml == "" {
		return ""
	}
	return sum([]byte(xml))
}
func bytesWithoutBOM(b []byte) []byte {
	return []byte(strings.TrimPrefix(strings.TrimSpace(string(b)), "\ufeff"))
}
func inspectTask(ctx context.Context, o Options, d windowsTask) (taskState, error) {
	return taskCommand(ctx, o, "inspect", d, "")
}
func acceptTask(state taskState, home string, old, pending *manifest) error {
	if !state.Exists {
		return nil
	}
	for _, m := range []*manifest{old, pending} {
		if m == nil {
			continue
		}
		raw, e := windowsDefinition(home, m.Node, m.Release)
		if e != nil {
			return e
		}
		var expected windowsTask
		_ = json.Unmarshal(raw, &expected)
		if expected == state.Definition {
			return nil
		}
	}
	return errors.New("custom or modified managed delivery task; refusing replacement")
}
func windowsReadUnit(home string) ([]byte, error) {
	if e := checkPath(home, root(home)); e != nil {
		return nil, e
	}
	b, e := readPrivate(unitPath(home), 64<<10)
	if errors.Is(e, os.ErrNotExist) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if len(b) == 0 {
		return nil, errors.New("empty managed delivery task definition")
	}
	return b, nil
}
func waitTask(ctx context.Context, o Options, d windowsTask, running bool) (taskState, error) {
	for {
		state, e := inspectTask(ctx, o, d)
		if e != nil {
			return state, e
		}
		if running && state.Exists && state.State == 4 && state.Definition == d && state.Enabled {
			return state, nil
		}
		if !running && (!state.Exists || state.State != 4 && state.State != 2) {
			return state, nil
		}
		timer := time.NewTimer(150 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return state, errors.New("managed delivery Task Scheduler activation verification timed out; setup pending")
		case <-timer.C:
		}
	}
}

// COM is called in a short-lived, hidden PowerShell process through Options.Run,
// preserving an isolated service boundary for tests. Untrusted values are JSON
// data, not interpolated code. Mutations compare the exact inspected XML again.
const taskScript = `$ErrorActionPreference='Stop'
$ProgressPreference='SilentlyContinue'
[Console]::OutputEncoding=New-Object System.Text.UTF8Encoding($false)
$i=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($inputData)) | ConvertFrom-Json
$d=$i.definition
$s=New-Object -ComObject Schedule.Service
$s.Connect()
$f=$s.GetFolder('\')
function HashXML([string]$xml) { $hash=[Security.Cryptography.SHA256]::Create();try{return ([BitConverter]::ToString($hash.ComputeHash([Text.Encoding]::UTF8.GetBytes($xml)))).Replace('-','').ToLowerInvariant()}finally{$hash.Dispose()} }
function SafeTask($t) {
 try {
  $sd=New-Object System.Security.AccessControl.CommonSecurityDescriptor($false,$false,$t.GetSecurityDescriptor(7))
  if($sd.Owner.Value -ne $d.sid -or $null -eq $sd.DiscretionaryAcl){return $false}
  foreach($a in $sd.DiscretionaryAcl) { if($a.AceQualifier -eq [Security.AccessControl.AceQualifier]::AccessAllowed -and $a.SecurityIdentifier.Value -notin @($d.sid,'S-1-5-18','S-1-5-32-544')) {return $false} }
  return $true
 } catch {return $false}
}
function SIDValue($value) { try {return (New-Object Security.Principal.SecurityIdentifier($value)).Value} catch {try {return (New-Object Security.Principal.NTAccount($value)).Translate([Security.Principal.SecurityIdentifier]).Value} catch {return ""}} }
function ProjectTask($t) {
 $x=$t.Definition
 $ok=(SIDValue $x.Principal.UserId) -eq $d.sid -and $x.Principal.LogonType -eq 3 -and $x.Principal.RunLevel -eq 0 -and $x.Actions.Count -eq 1 -and $x.Triggers.Count -eq 1
 if($ok){$a=$x.Actions.Item(1);$tr=$x.Triggers.Item(1);$ok=$a.Type -eq 0 -and $tr.Type -eq 9 -and (SIDValue $tr.UserId) -eq $d.sid -and $tr.Enabled -and [string]$tr.Delay -eq '' -and [string]$tr.StartBoundary -eq '' -and [string]$tr.EndBoundary -eq '' -and [string]$tr.Repetition.Interval -eq '' -and [string]$tr.Repetition.Duration -eq '' -and !$tr.Repetition.StopAtDurationEnd}
 $z=$x.Settings
 $ok=$ok -and $z.MultipleInstances -eq 2 -and !$z.DisallowStartIfOnBatteries -and !$z.StopIfGoingOnBatteries -and $z.AllowHardTerminate -and $z.StartWhenAvailable -and !$z.RunOnlyIfNetworkAvailable -and $z.AllowDemandStart -and $z.Hidden -and !$z.RunOnlyIfIdle -and !$z.WakeToRun -and $z.ExecutionTimeLimit -eq 'PT0S' -and $z.RestartInterval -eq 'PT1M' -and $z.RestartCount -eq 999 -and [string]$z.DeleteExpiredTaskAfter -eq '' -and $z.Priority -eq 7 -and [string]$z.NetworkSettings.Id -eq '' -and [string]$z.NetworkSettings.Name -eq ''
 if(!$ok){return $null}
 if((SIDValue $x.RegistrationInfo.Author) -ne $d.sid -or $x.RegistrationInfo.Description -ne 'vecnet managed standalone Codex delivery v1'){return $null}
 return @{schema=1;name=$t.Name;sid=(SIDValue $x.Principal.UserId);description=$x.RegistrationInfo.Description;command=$a.Path;arguments=$a.Arguments;directory=$a.WorkingDirectory}
}
$tasks=@($f.GetTasks(1))
$found=@($tasks | Where-Object {$_.Name -eq $d.name})
if($found.Count -gt 1){throw 'ambiguous task'}
$t=$null
if($found.Count -eq 1){$t=$found[0]}
$conflicts=@($tasks | Where-Object {($_.Name -like 'vecnet-launcher*' -or $_.Name -like 'vecnet-delivery*' -or ($_.Name -like 'vecnet-codex-delivery*' -and $_.Name -ne $d.name)) -and ($_.State -eq 4 -or $_.State -eq 2)} | ForEach-Object {$_.Name})
if($i.op -eq 'verify') {
 if(!$t -or $t.State -ne 4 -or !(SafeTask $t) -or !(ProjectTask $t)){throw 'task not active'}
 $ack=Join-Path (Split-Path $d.directory) 'delivery-active.json'
 $deadline=[DateTime]::UtcNow.AddSeconds(8)
 do {
  if(Test-Path -LiteralPath $ack){$v=[IO.File]::ReadAllText($ack)|ConvertFrom-Json;if($v.schema -eq 1 -and $v.release -eq $d.directory){$p=Get-Process -Id $v.pid -ErrorAction SilentlyContinue;if($p -and $p.Path -eq $v.node){break}}}
  Start-Sleep -Milliseconds 150
 } while([DateTime]::UtcNow -lt $deadline)
 if(!$p){throw 'delivery child did not acknowledge startup'}
 $instances=@($s.GetRunningTasks(1) | Where-Object {$_.Path -eq ('\'+$d.name)});if($instances.Count -ne 1){throw 'ambiguous running instance'}
 $child=Get-CimInstance Win32_Process -Filter ('ProcessId='+$v.pid)
 if(!$child -or $child.ParentProcessId -ne $instances[0].EnginePID){throw 'delivery acknowledgement is not the scheduled child'}
 Start-Sleep -Milliseconds 500
 $t=$f.GetTask($d.name)
 if($t.State -ne 4 -or $p.HasExited){throw 'delivery exited during activation'}
}
elseif($i.op -ne 'inspect') {
 if($conflicts.Count -gt 0){throw 'conflicting task'}
 if($t){if(!(SafeTask $t) -or !(ProjectTask $t) -or (HashXML $t.Xml) -cne $i.expected){throw 'task changed after inspection'}}elseif($i.expected -ne ''){throw 'task disappeared'}
 switch($i.op){
  'stop' {if($t){$t.Stop(0)}}
  'register' {
   if($t -and ($t.State -eq 4 -or $t.State -eq 2)){throw 'task must stop before update'}
   $x=$s.NewTask(0)
   $x.RegistrationInfo.Author=$d.sid
   $x.RegistrationInfo.Description=$d.description
   $x.Principal.UserId=$d.sid;$x.Principal.LogonType=3;$x.Principal.RunLevel=0
   $tr=$x.Triggers.Create(9);$tr.UserId=$d.sid;$tr.Enabled=$true
   $z=$x.Settings
   $z.MultipleInstances=2;$z.DisallowStartIfOnBatteries=$false;$z.StopIfGoingOnBatteries=$false;$z.AllowHardTerminate=$true;$z.StartWhenAvailable=$true;$z.RunOnlyIfNetworkAvailable=$false;$z.AllowDemandStart=$true;$z.Enabled=$true;$z.Hidden=$true;$z.RunOnlyIfIdle=$false;$z.WakeToRun=$false;$z.ExecutionTimeLimit='PT0S';$z.RestartInterval='PT1M';$z.RestartCount=999
   $a=$x.Actions.Create(0);$a.Path=$d.command;$a.Arguments=$d.arguments;$a.WorkingDirectory=$d.directory
   $sddl='O:'+$d.sid+'G:'+$d.sid+'D:P(A;;GA;;;'+$d.sid+')(A;;GA;;;SY)(A;;GA;;;BA)'
   $flags=2;if($t){$flags=4}
   $t=$f.RegisterTaskDefinition($d.name,$x,$flags,$d.sid,$null,3,$sddl)
  }
  'enable' {if(!$t){throw 'missing task'};$t.Enabled=$true}
  'start' {if(!$t){throw 'missing task'};[void]$t.Run($null)}
  default {throw 'unknown task operation'}
 }
 if($t){$t=$f.GetTask($d.name)}
}
if(!$t){@{exists=$false;safe=$true;enabled=$false;state=0;xml='';definition=$null;conflicts=$conflicts} | ConvertTo-Json -Compress -Depth 6}
else {$projection=ProjectTask $t;@{exists=$true;safe=((SafeTask $t) -and $null -ne $projection);enabled=[bool]$t.Enabled;state=[int]$t.State;xml=[string]$t.Xml;definition=$projection;conflicts=$conflicts} | ConvertTo-Json -Compress -Depth 6}
`
