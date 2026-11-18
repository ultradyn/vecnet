param([Parameter(Mandatory=$true)][string]$Request)
$ErrorActionPreference='Stop'
# Same suspended-create -> assign -> resume and kill-on-close JobObject pattern
# as internal/localruntime/windows_assets.go. CI-only, no runtime edits.
Add-Type -TypeDefinition @'
using System;
using System.Diagnostics;
using System.Runtime.InteropServices;
using System.Text;
using System.Threading.Tasks;
public static class CiOwnedJob {
 [StructLayout(LayoutKind.Sequential)] struct Basic { public long User,Kernel; public uint Flags; public UIntPtr Min,Max; public uint Active; public UIntPtr Affinity; public uint Priority,Scheduling; }
 [StructLayout(LayoutKind.Sequential)] struct IO { public ulong A,B,C,D,E,F; }
 [StructLayout(LayoutKind.Sequential)] struct Extended { public Basic Basic; public IO IO; public UIntPtr A,B,C,D; }
 [StructLayout(LayoutKind.Sequential,CharSet=CharSet.Unicode)] struct Startup { public uint cb; public string reserved,desktop,title; public uint x,y,xSize,ySize,xCount,yCount,fill,flags; public ushort show,reserved2; public IntPtr pointer,input,output,error; }
 [StructLayout(LayoutKind.Sequential)] struct Info { public IntPtr process,thread; public uint pid,tid; }
 [StructLayout(LayoutKind.Sequential)] struct Accounting { public long A,B,C,D; public uint Faults,Total,Active,Terminated; }
 [DllImport("kernel32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern IntPtr CreateJobObject(IntPtr a,string n);
 [DllImport("kernel32.dll",SetLastError=true)] static extern bool SetInformationJobObject(IntPtr j,int c,ref Extended i,uint n);
 [DllImport("kernel32.dll",SetLastError=true)] static extern bool QueryInformationJobObject(IntPtr j,int c,out Accounting i,uint n,IntPtr returned);
 [DllImport("kernel32.dll",SetLastError=true)] static extern bool AssignProcessToJobObject(IntPtr j,IntPtr p);
 [DllImport("kernel32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern bool CreateProcess(string app,StringBuilder command,IntPtr pa,IntPtr ta,bool inherit,uint flags,IntPtr env,string cwd,ref Startup s,out Info i);
 [DllImport("kernel32.dll")] static extern uint ResumeThread(IntPtr t);
 [DllImport("kernel32.dll")] static extern bool TerminateProcess(IntPtr p,uint code);
 [DllImport("kernel32.dll")] static extern bool TerminateJobObject(IntPtr p,uint code);
 [DllImport("kernel32.dll")] static extern uint WaitForSingleObject(IntPtr p,uint ms);
 [DllImport("kernel32.dll")] static extern bool GetExitCodeProcess(IntPtr p,out uint code);
 [DllImport("kernel32.dll")] static extern IntPtr GetStdHandle(int n);
 [DllImport("kernel32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern IntPtr CreateFile(string name,uint access,uint share,IntPtr security,uint creation,uint flags,IntPtr template);
 [DllImport("kernel32.dll")] static extern bool SetHandleInformation(IntPtr h,uint mask,uint flags);
 [DllImport("kernel32.dll")] static extern bool CloseHandle(IntPtr h);
 static void Check(bool b) {if(!b) throw new System.ComponentModel.Win32Exception();}
 static string Quote(string s) {
  var result=new StringBuilder("\"");int slashes=0;
  foreach(char c in s) {
   if(c=='\\'){slashes++;continue;}
   if(c=='"'){result.Append('\\',slashes*2+1);result.Append('"');slashes=0;continue;}
   result.Append('\\',slashes);slashes=0;result.Append(c);
  }
  result.Append('\\',slashes*2);result.Append('"');return result.ToString();
 }
 public static int Run(string file,string[] args,int ms) {
  var job=CreateJobObject(IntPtr.Zero,null);Check(job!=IntPtr.Zero);
  var info=new Info(); bool started=false;var input=IntPtr.Zero;
  try {
   var limits=new Extended();limits.Basic.Flags=0x2000;
   Check(SetInformationJobObject(job,9,ref limits,(uint)Marshal.SizeOf(limits)));
   var startup=new Startup();startup.cb=(uint)Marshal.SizeOf(startup);startup.flags=0x100;
   SetHandleInformation(GetStdHandle(-10),1,0);
   input=CreateFile("NUL",0x80000000,3,IntPtr.Zero,3,0,IntPtr.Zero);Check(input!=new IntPtr(-1));Check(SetHandleInformation(input,1,1));
   startup.input=input;startup.output=GetStdHandle(-11);startup.error=GetStdHandle(-12);
   var command=new StringBuilder(Quote(file));foreach(var arg in args)command.Append(" "+Quote(arg));
   Check(CreateProcess(file,command,IntPtr.Zero,IntPtr.Zero,true,0x08000004,IntPtr.Zero,null,ref startup,out info));
   Check(AssignProcessToJobObject(job,info.process));Check(ResumeThread(info.thread)!=UInt32.MaxValue);started=true;
   var cancel=Task.Run(()=>Console.In.ReadLine());var watch=Stopwatch.StartNew();
   while(true) {
    if(WaitForSingleObject(info.process,0)==0) {uint code;Check(GetExitCodeProcess(info.process,out code));return unchecked((int)code);}
    if(cancel.IsCompleted)return 143;
    if(watch.ElapsedMilliseconds>=ms)return 124;
    System.Threading.Thread.Sleep(10);
   }
  } finally {
   if(!started && info.process!=IntPtr.Zero)TerminateProcess(info.process,1);
   TerminateJobObject(job,1);
   var wait=Stopwatch.StartNew();Accounting accounting;
   while(QueryInformationJobObject(job,1,out accounting,(uint)Marshal.SizeOf(typeof(Accounting)),IntPtr.Zero) && accounting.Active!=0) {
    if(wait.ElapsedMilliseconds>2000)throw new Exception("owned job did not drain");
    System.Threading.Thread.Sleep(10);
   }
   CloseHandle(job);if(input!=IntPtr.Zero && input!=new IntPtr(-1))CloseHandle(input);if(info.thread!=IntPtr.Zero)CloseHandle(info.thread);if(info.process!=IntPtr.Zero)CloseHandle(info.process);
  }
 }
}
'@
try {
 $data=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($Request)) | ConvertFrom-Json
 $code=[CiOwnedJob]::Run($data.file,[string[]]$data.args,[int]$data.ms)
 exit $code
} catch { [Console]::Error.WriteLine($_);exit 1 }
