//go:build windows

package localruntime

// The hidden scheduler action owns a kill-on-close job containing Node. A
// scheduler stop therefore cannot strand delivery from the previous release.
const windowsHost = `param([Parameter(Mandatory=$true)][string]$Node,[Parameter(Mandatory=$true)][string]$HomePath,[Parameter(Mandatory=$true)][string]$WindowsRoot,[Parameter(Mandatory=$true)][string]$ReleasePath)
$ErrorActionPreference='Stop'
$ProgressPreference='SilentlyContinue'
$source=@"
using System;
using System.Runtime.InteropServices;
public static class VecnetDeliveryJob {
 [StructLayout(LayoutKind.Sequential)] struct Basic { public Int64 PerProcessUserTimeLimit,PerJobUserTimeLimit; public UInt32 LimitFlags; public UIntPtr MinimumWorkingSetSize,MaximumWorkingSetSize; public UInt32 ActiveProcessLimit; public UIntPtr Affinity; public UInt32 PriorityClass,SchedulingClass; }
 [StructLayout(LayoutKind.Sequential)] struct IO { public UInt64 ReadOperationCount,WriteOperationCount,OtherOperationCount,ReadTransferCount,WriteTransferCount,OtherTransferCount; }
 [StructLayout(LayoutKind.Sequential)] struct Extended { public Basic BasicLimitInformation; public IO IoInfo; public UIntPtr ProcessMemoryLimit,JobMemoryLimit,PeakProcessMemoryUsed,PeakJobMemoryUsed; }
 [StructLayout(LayoutKind.Sequential,CharSet=CharSet.Unicode)] struct Startup { public UInt32 cb; public string reserved,desktop,title; public UInt32 x,y,xSize,ySize,xCountChars,yCountChars,fillAttribute,flags; public UInt16 showWindow,reserved2; public IntPtr reservedPointer,stdInput,stdOutput,stdError; }
 [StructLayout(LayoutKind.Sequential)] struct ProcessInfo { public IntPtr process,thread; public UInt32 processId,threadId; }
 [DllImport("kernel32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern IntPtr CreateJobObject(IntPtr attr,string name);
 [DllImport("kernel32.dll",SetLastError=true)] static extern bool SetInformationJobObject(IntPtr job,int cls,ref Extended info,uint size);
 [DllImport("kernel32.dll",SetLastError=true)] static extern bool AssignProcessToJobObject(IntPtr job,IntPtr process);
 [DllImport("kernel32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern bool CreateProcess(string app,System.Text.StringBuilder command,IntPtr processAttributes,IntPtr threadAttributes,bool inheritHandles,UInt32 flags,IntPtr environment,string cwd,ref Startup startup,out ProcessInfo info);
 [DllImport("kernel32.dll",SetLastError=true)] static extern UInt32 ResumeThread(IntPtr thread);
 [DllImport("kernel32.dll")] static extern bool TerminateProcess(IntPtr process,UInt32 code);
 [DllImport("kernel32.dll",SetLastError=true)] static extern UInt32 WaitForSingleObject(IntPtr process,UInt32 milliseconds);
 [DllImport("kernel32.dll",SetLastError=true)] static extern bool GetExitCodeProcess(IntPtr process,out UInt32 code);
 [DllImport("kernel32.dll")] public static extern bool CloseHandle(IntPtr handle);
 public static IntPtr Create() { var h=CreateJobObject(IntPtr.Zero,null); if(h==IntPtr.Zero) throw new System.ComponentModel.Win32Exception(); var x=new Extended(); x.BasicLimitInformation.LimitFlags=0x2000; if(!SetInformationJobObject(h,9,ref x,(uint)Marshal.SizeOf(x))) { CloseHandle(h);throw new System.ComponentModel.Win32Exception(); } return h; }
 public static IntPtr Start(IntPtr job,string app,string command,string cwd,string[] env) {
  var startup=new Startup(); startup.cb=(uint)Marshal.SizeOf(startup); var info=new ProcessInfo(); var block=Marshal.StringToHGlobalUni(String.Join("\0",env)+"\0\0");
  try {
   if(!CreateProcess(app,new System.Text.StringBuilder(command),IntPtr.Zero,IntPtr.Zero,false,0x08000404,block,cwd,ref startup,out info)) throw new System.ComponentModel.Win32Exception();
   if(!AssignProcessToJobObject(job,info.process) || ResumeThread(info.thread)==UInt32.MaxValue) {TerminateProcess(info.process,1);CloseHandle(info.process);throw new System.ComponentModel.Win32Exception();}
   return info.process;
  } finally {Marshal.FreeHGlobal(block);if(info.thread!=IntPtr.Zero)CloseHandle(info.thread);}
 }
 public static int Wait(IntPtr process) { UInt32 code; if(WaitForSingleObject(process,UInt32.MaxValue)!=0 || !GetExitCodeProcess(process,out code)) throw new System.ComponentModel.Win32Exception(); return unchecked((int)code); }
}
"@
Add-Type -TypeDefinition $source
function QuoteArg([string]$s) { return '"'+[regex]::Replace([regex]::Replace($s,'(\\*)"','$1$1\"'),'(\\+)$','$1$1')+'"' }
$job=[VecnetDeliveryJob]::Create()
$child=[IntPtr]::Zero
try {
 $command=(QuoteArg $Node)+' --disable-warning=ExperimentalWarning --experimental-strip-types '+(QuoteArg (Join-Path $ReleasePath 'windows-delivery.mjs'))+' '+(QuoteArg $HomePath)+' '+(QuoteArg $WindowsRoot)
 $envBlock=@(('HOME='+$HomePath),('PATH='+(Split-Path $Node)+';'+(Join-Path $WindowsRoot 'System32')),('SystemRoot='+$WindowsRoot),('USERPROFILE='+$HomePath),'VECNET_LOCAL_MAINTENANCE=0',('WINDIR='+$WindowsRoot))
 $child=[VecnetDeliveryJob]::Start($job,$Node,$command,$ReleasePath,$envBlock)
 $code=[VecnetDeliveryJob]::Wait($child)
} catch { [Console]::Error.WriteLine('managed Codex delivery host failed'); $code=1 }
finally { [void][VecnetDeliveryJob]::CloseHandle($job);if($child -ne [IntPtr]::Zero){[void][VecnetDeliveryJob]::CloseHandle($child)} }
exit $code
`

// No credential, binding or delivery intent is embedded in these artifacts.
const windowsBootstrap = `import { dirname, join, isAbsolute } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { writeFileSync, renameSync } from 'node:fs';
const release = dirname(fileURLToPath(import.meta.url));
const [home, windowsRoot] = process.argv.slice(2);
if (process.argv.length !== 4 || !isAbsolute(home) || !isAbsolute(windowsRoot)) throw new Error('invalid managed delivery environment');
const entry = join(release, 'connectors/launcher/src/codex/delivery-main.ts');
process.env = { HOME: home, USERPROFILE: home, SystemRoot: windowsRoot, WINDIR: windowsRoot,
 PATH: dirname(process.execPath) + ';' + join(windowsRoot, 'System32'),
 VECNET_BIN: join(release, 'vecnet.exe'), VECNET_LOCAL_MAINTENANCE: '0' };
process.argv = [process.execPath, entry];
await import(pathToFileURL(entry).href);
// Acknowledgement proves the action reached its staged module graph, not that
// authority exists or any delivery is ready. Task liveness is checked separately.
const ack = join(home, '.local/share/vecnet-codex/delivery-active.json');
const tmp = ack + '.' + process.pid + '.tmp';
writeFileSync(tmp, JSON.stringify({ schema: 1, pid: process.pid, release, node: process.execPath }), { flag: 'wx', mode: 0o600 });
renameSync(tmp, ack);
`

func addPlatformAssets(files map[string][]byte) {
	files["windows-host.ps1"] = []byte(windowsHost)
	files["windows-delivery.mjs"] = []byte(windowsBootstrap)
}
